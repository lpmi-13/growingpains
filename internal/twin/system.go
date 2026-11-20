// Package twin is a fast simulator of the shop. It plays a storyline against the same
// architect code and contracts, with the signal source, actuator, and clock swapped, at about a
// hundred times real speed. It validates seeds, predicts each seed's growth path, and tests
// rule and contract changes in CI before any playground starts.
package twin

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/signals"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/traffic"
)

// System is the simulated shop: its architecture as the actuator left it, and the load on it.
type System struct {
	costs  Costs
	ix     *catalog.Index
	story  *storyline.Storyline
	picker *traffic.Picker
	r      *rand.Rand

	// Architecture, as actions change it.
	LegacyWeight, FleetWeight float64
	LegacyUp                  bool
	FleetPods                 int
	FleetDesired              int
	FleetMax                  int
	podsPendingSince          time.Time
	lastScaleNeed             time.Time
	ReplicaUp                 bool
	ReplicaPct                float64
	PrimaryWeight             float64 // the primary's weight in the read pool, against 1 per replica
	Pooled                    bool    // the fleet connects through PgBouncer

	lastRate, lastDBSecondsPerRequest   float64
	lastJourneyRates, lastJourneyErrors map[string]float64
	// The last step's cache hits, as shares of all listing and of all product reads (bypass included).
	lastHitListing, lastHitDetail float64
	CacheUp                       bool
	CachePct                      float64
	PrimaryCores                  float64
	ReplicaCores                  float64
	DBUnit                        float64 // the cap of one db.medium instance, in playground cores

	// Shape, recomputed every repeatEvery.
	repeat        map[string]float64
	repeatAt      time.Time
	listingHitAvg float64

	last signals.Values
}

const repeatEvery = 15 * time.Second

// meanSteps matches the crowd's mean session length.
const meanSteps = 5.0

// tokenNotReplayed is the share of token-carrying reads routed to a replica that go back to the
// primary because the replica has not replayed their write yet. Measured in a flash sale on the
// playground (2026-11-15): 14.7 a second of about 110.
const tokenNotReplayed = 0.13

// PlaygroundDBCores is one db.medium database instance on the playground: the CPU quota of the
// primary's and each replica's unit, in playground cores. The plan sizes db.medium at 1.5 cores,
// and the architect's ledger still books it so, as it books the genesis API at one core under a
// 40% cap: these are the storyline's capacity units. The re-fitted twin puts the cap here, where
// the storyline takes fleet → replica → cache with the widest margin (docs/calibration.md).
// rootfs_test.go checks the images agree.
const PlaygroundDBCores = 0.65

// NewSystem is genesis: one API process, one database, nothing else. Each database instance's
// CPU cap is dbCores, in playground cores.
func NewSystem(ix *catalog.Index, story *storyline.Storyline, seed uint64, costs Costs, dbCores float64) *System {
	return &System{
		costs: costs, ix: ix, story: story, picker: traffic.NewPicker(ix, story, seed),
		r:            rng.Stream(seed, "twin", 0),
		LegacyWeight: 100, LegacyUp: true, FleetMax: 6, PrimaryCores: dbCores, ReplicaCores: dbCores, DBUnit: dbCores,
		repeat: map[string]float64{},
	}
}

// load is the traffic broken into request rates by operation.
type load struct {
	rate                                                    float64
	home, listing, detail, reread, search, searchKeystrokes float64
	order, review, create, patch, delete, variants          float64
	anonReads, tokenShare                                   float64
	listingDB                                               float64 // mean execution ms per listing view
	listingHotDB                                            float64
}

func (s *System) loadAt(st storyline.State) load {
	m := st.Mix
	R := st.Rate
	l := load{rate: R}
	shopper := math.Max(0, 1-m.AnonymousShare-m.SellerShare) * R
	w := m.Home + m.Listing + m.Detail + m.Search + m.Order + m.Review
	if w <= 0 {
		w = 1
	}
	l.home = shopper * m.Home / w
	l.listing = shopper * m.Listing / w
	l.detail = shopper * m.Detail / w
	l.search = shopper * m.Search / w
	// Search-as-you-type sends a request per keystroke from the third.
	l.searchKeystrokes = l.search * m.SearchKeystroke * 6
	l.order = shopper * m.Order / w
	l.review = shopper * m.Review / w
	anon := R * m.AnonymousShare
	l.listing += anon * m.AnonListing
	l.home += anon * (1 - m.AnonListing)
	l.anonReads = anon
	actions := R * m.SellerShare / 2
	sw := m.Create + m.Patch + m.Variants + m.Delete + m.Reread
	if sw <= 0 {
		sw = 1
	}
	l.create = actions * m.Create / sw
	l.patch = actions * m.Patch / sw
	l.variants = actions * m.Variants / sw
	l.delete = actions * m.Delete / sw
	l.reread = actions * (1 + m.Reread/sw)
	// Reads carrying a read-your-writes token. A shopper's read carries one once an earlier step
	// in the session wrote (sessions last a few seconds, within the token's ten), and a seller's
	// re-read always does. At the genesis mix this predicts about 18%; the edge measured 19-20%.
	pw := (m.Order + m.Review) / w
	f := 0.0
	for i := 1; i <= int(meanSteps); i++ {
		f += 1 - math.Pow(1-pw, float64(i-1))
	}
	f /= meanSteps
	shopperReads := shopper * (m.Home + m.Listing + m.Detail + m.Search) / w
	allReads := math.Max(1e-9, shopperReads+anon+l.reread)
	l.tokenShare = (shopperReads*f + l.reread) / allReads
	// The mean database time of a listing view, by sampling the picker.
	var sum, hot, nHot float64
	const samples = 400
	for range samples {
		lv := s.picker.Listing(s.r, m)
		c := s.costs.ListingDB(lv)
		sum += c
		if lv.Hot {
			hot += c
			nHot++
		}
	}
	l.listingDB = sum / samples
	if nHot > 0 {
		l.listingHotDB = hot / nHot
	}
	return l
}

// Explain breaks the load at one moment into request rates and costs by operation.
func (s *System) Explain(st storyline.State) []string {
	l := s.loadAt(st)
	c := s.costs
	rows := []struct {
		name          string
		rate, api, db float64
	}{
		{"home", l.home, c.APIHome, c.DBHome},
		{"listing", l.listing, c.APIListing, l.listingDB},
		{"detail", l.detail, c.APIDetail, c.DBDetail},
		{"reread", l.reread, c.APIReread, c.DBDetail},
		{"search", l.search + l.searchKeystrokes, c.APISearch, c.DBSearch},
		{"order", l.order, c.APIOrder, c.DBOrder},
		{"review", l.review, c.APIReview, c.DBReview},
		{"create", l.create, c.APICreate, c.DBCreate},
		{"patch", l.patch, c.APIPatch, c.DBPatch},
		{"delete", l.delete, c.APIDelete, c.DBDelete},
		{"variants", l.variants, c.APIVariants, c.DBVariants},
	}
	var out []string
	var api, db float64
	for _, r := range rows {
		api += r.rate * r.api
		db += r.rate * r.db * c.DBOverhead
		out = append(out, fmt.Sprintf("%-9s %7.1f/s  api %5.2f ms %5.3f cores   db %6.2f ms %5.3f cores", r.name, r.rate, r.api, r.rate*r.api/1000, r.db*c.DBOverhead, r.rate*r.db*c.DBOverhead/1000))
	}
	out = append(out, fmt.Sprintf("total %.0f req/s: api %.2f cores (%.2f ms/req), db %.2f cores (%.2f ms/req, plus %.2f cores at rest), token share %.2f, listing db exec %.2f ms",
		l.rate, api/1000, api/l.rate, db/1000+c.DBBaseCores, db/l.rate, c.DBBaseCores, l.tokenShare, l.listingDB))
	// The listing's sort statements, to compare with pg_stat_statements (calls and ms per call).
	var n, ms [4]float64
	const samples = 4000
	for range samples {
		lv := s.picker.Listing(s.r, st.Mix)
		n[lv.Sort]++
		ms[lv.Sort] += c.ListingDB(lv) - c.DBFacets
	}
	line := "listing sorts:"
	for i, name := range traffic.SortNames {
		if n[i] > 0 {
			line += fmt.Sprintf("  %s %.0f%% %.2f ms", name, 100*n[i]/samples, ms[i]/n[i])
		}
	}
	return append(out, line+fmt.Sprintf("  (facets %.2f ms on each)", c.DBFacets))
}

// sampleRepeat draws a minute of listing and product views and measures repetition the way the
// edge does: 1 - distinct keys / views.
func (s *System) sampleRepeat(st storyline.State, l load) {
	window := 60.0
	nl := int(math.Min(l.listing*window, 60000))
	nd := int(math.Min((l.detail+l.reread)*window, 60000))
	listing := make(map[string]int, nl)
	detail := make(map[int64]int, nd)
	listing10 := 0
	seen10 := map[string]bool{}
	for i := range nl {
		lv := s.picker.Listing(s.r, st.Mix)
		k := lv.Key()
		listing[k]++
		// A listing cache entry lives 10 s: count repeats within the last sixth of the window.
		if i >= nl*5/6 {
			if seen10[k] {
				listing10++
			}
			seen10[k] = true
		}
	}
	for range nd {
		id, _ := s.picker.Product(s.r, st.Mix)
		detail[id]++
	}
	ratio := func(distinct, total int) float64 {
		if total == 0 {
			return 0
		}
		return 1 - float64(distinct)/float64(total)
	}
	s.repeat["listing"] = ratio(len(listing), nl)
	s.repeat["detail"] = ratio(len(detail), nd)
	s.repeat["cacheable"] = ratio(len(listing)+len(detail), nl+nd)
	if n := nl - nl*5/6; n > 0 {
		s.listingHitAvg = float64(listing10) / float64(n)
	}
}

// Step advances the simulated shop to time now and returns its signals.
func (s *System) Step(st storyline.State, now time.Time) signals.Values {
	l := s.loadAt(st)
	if now.Sub(s.repeatAt) >= repeatEvery || s.repeatAt.IsZero() {
		s.sampleRepeat(st, l)
		s.repeatAt = now
	}
	c := s.costs
	reads := l.home + l.listing + l.detail + l.reread + l.search + l.searchKeystrokes

	// The cache serves repeated listing and product reads without a database round trip.
	cacheShare := 0.0
	hitDetail, hitListing := 0.0, 0.0
	if s.CacheUp {
		cacheShare = s.CachePct / 100 * (1 - l.tokenShare)
		hitDetail = s.repeat["detail"] * cacheShare
		hitListing = s.listingHitAvg * cacheShare
	}

	// API CPU, in cores.
	apiMS := l.home*c.APIHome + l.listing*(c.APIListing-hitListing*0.4) + l.detail*(c.APIDetail-hitDetail*c.APICacheHitSaving) +
		l.reread*(c.APIReread-hitDetail*c.APICacheHitSaving) + (l.search+l.searchKeystrokes)*c.APISearch + l.order*c.APIOrder +
		l.review*c.APIReview + l.create*c.APICreate + l.patch*c.APIPatch + l.delete*c.APIDelete + l.variants*c.APIVariants
	apiCores := apiMS / 1000

	// Database execution, in ms per second, split into what may go to a replica and what may not.
	routable := l.home*c.DBHome + l.listing*l.listingDB*(1-hitListing) + (l.detail+l.reread)*c.DBDetail*(1-hitDetail) +
		(l.search+l.searchKeystrokes)*c.DBSearch
	writeMS := l.order*c.DBOrder + l.review*c.DBReview + l.create*c.DBCreate + l.patch*c.DBPatch + l.delete*c.DBDelete + l.variants*c.DBVariants
	// Reads with a read-your-writes token never use the cache, so once it is up they are a larger
	// share of what reaches the database than of the traffic. They take the read pool like any
	// other read, and go back to the primary only when the chosen replica has not replayed their
	// write yet (shop/gp_shop/db.py, choose).
	tokenMS := l.tokenShare * (l.home*c.DBHome + l.listing*l.listingDB + (l.detail+l.reread)*c.DBDetail +
		(l.search+l.searchKeystrokes)*c.DBSearch)
	toReplica := 0.0
	if s.ReplicaUp {
		toReplica = (routable - tokenMS*tokenNotReplayed) * s.ReplicaPct / 100 / (1 + s.PrimaryWeight)
	}
	primaryCores := c.DBBaseCores + (routable-toReplica+writeMS)*c.DBOverhead/1000
	// Wall time in the database per request: execution plus about 0.2 ms of round trip per statement.
	s.lastRate = l.rate
	s.lastHitListing, s.lastHitDetail = hitListing, hitDetail
	s.lastDBSecondsPerRequest = ((routable+writeMS)/math.Max(l.rate, 1e-9) + 13*0.2) / 1000
	replicaCores := 0.0
	if s.ReplicaUp {
		replicaCores = c.DBBaseCores + toReplica*c.DBOverhead/1000
	}

	// API capacity and the HPA.
	legacyShare, fleetShare := 1.0, 0.0
	if tw := s.LegacyWeight + s.FleetWeight; tw > 0 && s.FleetPods > 0 {
		legacyShare, fleetShare = s.LegacyWeight/tw, s.FleetWeight/tw
	}
	if !s.LegacyUp {
		legacyShare, fleetShare = 0, 1
	}
	legacyDemand := apiCores * legacyShare
	fleetDemand := apiCores * fleetShare
	// Pods spread across the two compute nodes, and a node's pods share its 1.9 spare cores.
	fleetCap := math.Min(float64(s.FleetPods)*1.0, 1.9*math.Min(2, float64(s.FleetPods)))
	s.hpa(fleetDemand, now)

	legacyRho := 0.0
	if s.LegacyUp && legacyShare > 0 {
		legacyRho = legacyDemand / 1.0
	}
	fleetRho := 0.0
	if fleetCap > 0 {
		fleetRho = fleetDemand / fleetCap
	}
	// As the PromQL measures it: CPU actually used, over the sum of every instance's limit.
	apiUsed := math.Min(fleetDemand, fleetCap)
	apiLimits := float64(s.FleetPods)
	if s.LegacyUp {
		apiUsed += math.Min(legacyDemand, 1)
		apiLimits++
	}

	// Utilisation as the agents measure it: a cgroup can never use more than its quota.
	primaryRho := primaryCores / s.PrimaryCores
	replicaRho := 0.0
	if s.ReplicaUp {
		replicaRho = replicaCores / s.ReplicaCores
	}
	dbRho := math.Max(primaryRho, replicaRho)

	// Errors: work beyond a unit's capacity times out. A quota can also make long
	// searches time out before it is fully used; that risk belongs to the search
	// journey, rather than to every request equally.
	overload := func(rho float64) float64 {
		if rho <= 1 {
			return 0
		}
		return 1 - 1/rho
	}
	apiErr := legacyShare*overload(legacyRho) + fleetShare*overload(fleetRho)
	dbErr := overload(dbRho) * 0.8
	baseErr := math.Min(1, apiErr+dbErr*(1-apiErr))
	replicaSearchShare := s.searchReplicaShare(l.tokenShare)
	searchTail := replicaSearchShare * searchTimeoutUnderQuota(replicaCores, s.ReplicaCores, c.DBSearch/unindexedSearchMS)
	s.lastJourneyRates = l.journeyRates()
	s.lastJourneyErrors = make(map[string]float64, len(journeys))
	for _, journey := range journeys {
		s.lastJourneyErrors[journey] = baseErr
	}
	s.lastJourneyErrors["search"] = baseErr + (1-baseErr)*searchTail
	// Keep the architect's aggregate error signal in the same units as the crowd's requests.
	var attempted, failed float64
	for _, journey := range journeys {
		rate := s.lastJourneyRates[journey]
		attempted += rate
		failed += rate * s.lastJourneyErrors[journey]
	}
	errRatio := failed / math.Max(attempted, 1e-9)

	queue := func(rho, service float64) float64 {
		rho = math.Min(rho, 0.999)
		return service * math.Pow(rho, 8) / (1 - rho)
	}
	apiQueue := legacyShare*queue(legacyRho, 0.004) + fleetShare*queue(fleetRho, 0.004)

	totalDB := primaryCores + replicaCores
	totalDBCap := s.PrimaryCores
	if s.ReplicaUp {
		totalDBCap += s.ReplicaCores
	}
	dbReadShare := 1.0
	if all := routable + writeMS; all > 0 {
		dbReadShare = routable / all
	}
	clamp := func(x float64) float64 { return math.Min(x, 1) }
	v := signals.Values{
		"api.cpu_ratio":              apiUsed / math.Max(apiLimits, 1e-9),
		"api.queue_time_p95":         apiQueue,
		"api.cpu_per_request":        apiMS / 1000 / math.Max(l.rate, 1e-9),
		"api.side_effect_share":      0.02,
		"api.statements_per_request": 13,
		"api.top_client_share":       0.01,
		"api.instances":              float64(s.FleetPods + b2i(s.LegacyUp)),
		"api.config_version_min":     0,
		"api.error_ratio":            errRatio,
		"api.fleet_error_ratio":      overload(fleetRho) * b2f(s.FleetPods > 0),
		"traffic.rate":               l.rate,
		"traffic.listing_share":      l.listing / l.rate,
		"traffic.detail_share":       (l.detail + l.reread) / l.rate,
		"traffic.write_share":        (l.order + l.review + l.create + l.patch + l.delete + l.variants) / l.rate,
		"db.cpu_ratio":               clamp(dbRho),
		"db.primary_cpu_ratio":       clamp(primaryRho),
		"db.read_cpu_ratio":          clamp(totalDB/totalDBCap) * dbReadShare,
		"db.read_share":              dbReadShare,
		"db.search_share":            (l.search + l.searchKeystrokes) * c.DBSearch / math.Max(routable+writeMS, 1e-9),
		"db.seqscan_rows":            (l.search + l.searchKeystrokes) * float64(len(s.ix.Items)),
		"db.connections_ratio":       s.connections(),
		"db.lock_wait_share":         0,
		"db.blob_bytes_rate":         0,
		"db.repl_lag":                0.05 * float64(b2i(s.ReplicaUp)) * (1 + 5*overload(replicaRho)),
		"writes.seller_spread":       0.1,
		"reads.repeat_ratio":         s.repeat["cacheable"],
		"reads.anonymous_share":      l.anonReads / math.Max(reads, 1e-9),
		"reads.token_share":          l.tokenShare,
		"cache.hit_ratio":            (hitDetail*(l.detail+l.reread) + hitListing*l.listing) / math.Max(l.detail+l.reread+l.listing, 1e-9) / math.Max(cacheShare, 1e-9) * b2f(s.CacheUp),
		"slo.burn_1m":                errRatio / 0.005,
		"slo.burn_5m":                errRatio / 0.005,
		"slo.budget_remaining":       1,
	}
	s.last = v
	return v
}

// connections is the primary's backends against max_connections. By Little's law an instance
// has (requests per second x database seconds per request) queries in flight; its pool keeps
// about three times that open for bursts, up to its eight-connection limit per database.
// PgBouncer multiplexes all of them onto twenty server connections.
func (s *System) connections() float64 {
	if s.Pooled {
		return 0.25
	}
	instances := s.FleetPods + b2i(s.LegacyUp)
	if instances == 0 {
		return 0
	}
	perInstance := s.lastRate / float64(instances) * s.lastDBSecondsPerRequest
	open := math.Min(8, math.Max(1, math.Ceil(3*perInstance)+1))
	pools := 1 + b2i(s.ReplicaUp)
	return math.Min(1, (float64(instances*pools)*open+6)/100)
}

func (s *System) hpa(demand float64, now time.Time) {
	if s.FleetPods == 0 && s.FleetDesired == 0 {
		return
	}
	// 70% of a 600m request.
	target := 0.6 * 0.7
	need := int(math.Ceil(demand / target))
	need = max(2, min(s.FleetMax, need))
	switch {
	case need > s.FleetDesired:
		s.FleetDesired = need
		s.podsPendingSince = now
	case need < s.FleetDesired:
		// Scale-down waits out a five-minute stabilisation window.
		if s.lastScaleNeed.IsZero() {
			s.lastScaleNeed = now
		}
		if now.Sub(s.lastScaleNeed) >= 5*time.Minute {
			s.FleetDesired = need
			s.FleetPods = need
			s.lastScaleNeed = time.Time{}
		}
		return
	}
	s.lastScaleNeed = time.Time{}
	// New pods serve about 20 s after the HPA asks, with images preloaded.
	if s.FleetPods < s.FleetDesired && now.Sub(s.podsPendingSince) >= 20*time.Second {
		s.FleetPods = s.FleetDesired
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func b2f(b bool) float64 { return float64(b2i(b)) }
