// Package storyline describes how traffic grows during a run.
//
// A seed picks a storyline: a sequence of chapters, each turning one or two dials of the
// traffic. The crowd plays it, and the twin simulates it. The architect never sees it; the
// narrator reveals it only in the run report.
package storyline

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
)

// Mix is the shape of the traffic at one moment. Shares are fractions of all HTTP requests.
type Mix struct {
	// Where requests come from. Shopper sessions take whatever the other two leave.
	AnonymousShare float64 `json:"anonymous_share"` // direct, session-less GETs of home and listing pages
	SellerShare    float64 `json:"seller_share"`    // seller actors: writes and re-reads of their own products

	// What a shopper's next step is, as relative weights.
	Home    float64 `json:"home"`
	Listing float64 `json:"listing"`
	Detail  float64 `json:"detail"`
	Search  float64 `json:"search"`
	Order   float64 `json:"order"`
	Review  float64 `json:"review"`

	// Anonymous visitors: the share of their GETs that are listing pages rather than home.
	AnonListing float64 `json:"anon_listing"`

	// What a seller does next, as relative weights.
	Create   float64 `json:"create"`
	Patch    float64 `json:"patch"`
	Variants float64 `json:"variants"`
	Delete   float64 `json:"delete"`
	Reread   float64 `json:"reread"`

	ImageShare float64 `json:"image_share"` // of product creations that upload an image

	// Key distributions.
	DetailZipf    float64    `json:"detail_zipf"`   // exponent over the catalog for product views; 0 is uniform
	CategoryZipf  float64    `json:"category_zipf"` // exponent over categories for listing views
	PageMean      float64    `json:"page_mean"`     // mean listing page, geometric
	PageMax       int        `json:"page_max"`
	ListingFilter float64    `json:"listing_filter"` // share of listing views narrowed by a price range
	SortWeights   [4]float64 `json:"sort_weights"`   // newest, price_asc, price_desc, rating

	// A flash sale: this share of detail and listing views goes to the hot products and the sale
	// category, and the hot products' sellers edit them this many times more often.
	HotShare     float64 `json:"hot_share"`
	HotSellerMul float64 `json:"hot_seller_mul"`

	// Search behaviour.
	SearchKeystroke float64 `json:"search_keystroke"` // share of searches typed one keystroke at a time
	SearchTypo      float64 `json:"search_typo"`      // share of search words with a typo
	SearchFilter    float64 `json:"search_filter"`    // share of searches with filters

	OrderLinesMean float64 `json:"order_lines_mean"`
}

// Chapter is one period of a storyline.
type Chapter struct {
	Name     string        `json:"name"`
	Start    time.Duration `json:"start"`
	End      time.Duration `json:"end"`
	RateFrom float64       `json:"rate_from"` // HTTP requests per second at the start
	RateTo   float64       `json:"rate_to"`
	// Curve is how the rate moves from RateFrom to RateTo: "exp" (geometric, the default) or
	// "logistic" (word of mouth: slow, then fast, then slowing).
	Curve    string  `json:"curve,omitempty"`
	Midpoint float64 `json:"midpoint,omitempty"` // for logistic: the fraction of the chapter where growth is fastest
	Mix      Mix     `json:"mix"`                // the mix at the chapter's end; it moves from the previous chapter's
	// MixRamp is how long the mix takes to move from the previous chapter's; zero means the whole
	// chapter. A flash sale starts abruptly; a change in browsing habits does not.
	MixRamp time.Duration `json:"mix_ramp,omitempty"`
}

// Storyline is a whole run's traffic.
type Storyline struct {
	Name         string    `json:"name"`
	Seed         uint64    `json:"seed"`
	GenesisRate  float64   `json:"genesis_rate"`
	Genesis      Mix       `json:"genesis"`
	Chapters     []Chapter `json:"chapters"`
	HotProducts  []int64   `json:"hot_products"`
	SaleCategory int       `json:"sale_category"`
}

// State is the traffic at one moment of a run.
type State struct {
	Chapter int // -1 before the run starts
	Name    string
	Rate    float64
	Mix     Mix
}

// Duration is when the last chapter ends.
func (s *Storyline) Duration() time.Duration {
	return s.Chapters[len(s.Chapters)-1].End
}

// At returns the traffic at elapsed time t. Negative t is genesis, before the run starts.
func (s *Storyline) At(t time.Duration) State {
	if t < 0 {
		return State{Chapter: -1, Name: "Genesis", Rate: s.GenesisRate, Mix: s.Genesis}
	}
	prev := s.Genesis
	for i, c := range s.Chapters {
		if t < c.End || i == len(s.Chapters)-1 {
			f := 1.0
			if c.End > c.Start {
				f = math.Min(1, math.Max(0, float64(t-c.Start)/float64(c.End-c.Start)))
			}
			mf := f
			if c.MixRamp > 0 {
				mf = math.Min(1, math.Max(0, float64(t-c.Start)/float64(c.MixRamp)))
			}
			rate := expInterp(c.RateFrom, c.RateTo, f)
			if c.Curve == "logistic" {
				rate = logisticInterp(c.RateFrom, c.RateTo, f, c.Midpoint)
			}
			return State{Chapter: i, Name: c.Name, Rate: rate, Mix: lerpMix(prev, c.Mix, mf)}
		}
		prev = c.Mix
	}
	panic("unreachable")
}

// expInterp grows geometrically, which is how word of mouth grows.
func expInterp(a, b, f float64) float64 {
	if a <= 0 || b <= 0 {
		return a + (b-a)*f
	}
	return a * math.Pow(b/a, f)
}

// logisticInterp follows an S-curve with its steepest point at mid, scaled to start at a and end at b.
func logisticInterp(a, b, f, mid float64) float64 {
	const k = 6
	sig := func(x float64) float64 { return 1 / (1 + math.Exp(-k*(x-mid))) }
	lo, hi := sig(0), sig(1)
	return a + (b-a)*(sig(f)-lo)/(hi-lo)
}

func lerp(a, b, f float64) float64 { return a + (b-a)*f }

func lerpMix(a, b Mix, f float64) Mix {
	m := Mix{
		AnonymousShare:  lerp(a.AnonymousShare, b.AnonymousShare, f),
		SellerShare:     lerp(a.SellerShare, b.SellerShare, f),
		Home:            lerp(a.Home, b.Home, f),
		Listing:         lerp(a.Listing, b.Listing, f),
		Detail:          lerp(a.Detail, b.Detail, f),
		Search:          lerp(a.Search, b.Search, f),
		Order:           lerp(a.Order, b.Order, f),
		Review:          lerp(a.Review, b.Review, f),
		AnonListing:     lerp(a.AnonListing, b.AnonListing, f),
		Create:          lerp(a.Create, b.Create, f),
		Patch:           lerp(a.Patch, b.Patch, f),
		Variants:        lerp(a.Variants, b.Variants, f),
		Delete:          lerp(a.Delete, b.Delete, f),
		Reread:          lerp(a.Reread, b.Reread, f),
		ImageShare:      lerp(a.ImageShare, b.ImageShare, f),
		DetailZipf:      lerp(a.DetailZipf, b.DetailZipf, f),
		CategoryZipf:    lerp(a.CategoryZipf, b.CategoryZipf, f),
		PageMean:        lerp(a.PageMean, b.PageMean, f),
		ListingFilter:   lerp(a.ListingFilter, b.ListingFilter, f),
		HotShare:        lerp(a.HotShare, b.HotShare, f),
		HotSellerMul:    lerp(a.HotSellerMul, b.HotSellerMul, f),
		SearchKeystroke: lerp(a.SearchKeystroke, b.SearchKeystroke, f),
		SearchTypo:      lerp(a.SearchTypo, b.SearchTypo, f),
		SearchFilter:    lerp(a.SearchFilter, b.SearchFilter, f),
		OrderLinesMean:  lerp(a.OrderLinesMean, b.OrderLinesMean, f),
	}
	m.PageMax = b.PageMax
	if f < 0.5 {
		m.PageMax = a.PageMax
	}
	for i := range m.SortWeights {
		m.SortWeights[i] = lerp(a.SortWeights[i], b.SortWeights[i], f)
	}
	return m
}

// genesisMix is the shop's traffic before anything grows: 85% reads, uniform keys.
func genesisMix() Mix {
	return Mix{
		AnonymousShare: 0.15, SellerShare: 0.06,
		Home: 0.14, Listing: 0.40, Detail: 0.358, Search: 0.002, Order: 0.06, Review: 0.04,
		AnonListing: 0.5,
		Create:      0.30, Patch: 0.35, Variants: 0.05, Delete: 0.10, Reread: 0.20,
		ImageShare: 0.10,
		DetailZipf: 0, CategoryZipf: 0, PageMean: 1.6, PageMax: 4, ListingFilter: 0.1,
		SortWeights: [4]float64{0.62, 0.14, 0.12, 0.12},
		HotShare:    0, HotSellerMul: 1,
		SearchKeystroke: 0.05, SearchTypo: 0.03, SearchFilter: 0.2,
		OrderLinesMean: 1.5,
	}
}

func jitter(r *rand.Rand, v, frac float64) float64 { return v * (1 + rng.Uniform(r, -frac, frac)) }

func jitterDur(r *rand.Rand, d, spread time.Duration) time.Duration {
	return d + time.Duration(rng.Uniform(r, -1, 1)*float64(spread))
}

// WordGetsOut is the MVP storyline: volume, then long-tail browsing, then a flash sale. It is
// meant to grow an autoscaled fleet, a read replica, and a cache, in that order.
//
// The seed draws jitter of ±15% on rates, ±30 s on chapter boundaries, and the hot products.
func WordGetsOut(seed uint64, ix *catalog.Index) *Storyline {
	r := rng.Stream(seed, "storyline", 0)
	g := genesisMix()

	b1 := jitterDur(r, 5*time.Minute, 30*time.Second)
	b2 := jitterDur(r, 10*time.Minute, 30*time.Second)
	b3 := 15 * time.Minute
	end := 17 * time.Minute

	r1 := jitter(r, 320, 0.15)
	r2 := jitter(r, 520, 0.15)
	// The plan's flash sale peaks at 700. Against the playground's database, where the sale's
	// rating sorts are cheap, 700 left the cache late or missing once a replica had doubled read
	// capacity; 800 brings it in around 11:30, as the plan's table has it (docs/plan-deviations.md).
	r3 := jitter(r, 800, 0.15)

	// Chapter 1: steady mix, roughly exponential growth.
	c1 := g

	// Chapter 2: listing pages grow from 40% to 65% of requests, spread across categories,
	// sorts, price ranges, and deep pages, so few of them repeat.
	c2 := g
	c2.Listing, c2.Detail, c2.Home = 0.62, 0.20, 0.08
	c2.AnonListing = 0.85
	c2.PageMean, c2.PageMax = 3.5, 12
	c2.ListingFilter = 0.8
	c2.SortWeights = [4]float64{0.30, 0.25, 0.25, 0.20}

	// Chapter 3: a flash sale. Half of all detail and listing views go to 20 sale products and the
	// sale category, and their sellers edit them often.
	c3 := c2
	c3.HotShare = 0.55
	c3.HotSellerMul = 8
	c3.Detail, c3.Listing = 0.30, 0.52
	c3.ListingFilter = 0.2 // shoppers browse the sale rather than narrowing other categories

	s := &Storyline{
		Name:         "Word gets out",
		Seed:         seed,
		GenesisRate:  20,
		Genesis:      g,
		SaleCategory: ix.SaleCategoryID(),
		Chapters: []Chapter{
			// Word of mouth: about 190 req/s around 3:00, then slowing towards the chapter's end.
			{Name: "Word gets out", Start: 0, End: b1, RateFrom: 20, RateTo: r1, Mix: c1, Curve: "logistic", Midpoint: 0.55},
			// Browsing habits change over a couple of minutes, then hold.
			{Name: "Browsing season", Start: b1, End: b2, RateFrom: r1, RateTo: r2, Mix: c2, MixRamp: 90 * time.Second},
			{Name: "Flash sale", Start: b2, End: b3, RateFrom: r2, RateTo: r3, Mix: c3, MixRamp: 90 * time.Second},
			{Name: "Plateau", Start: b3, End: end, RateFrom: r3, RateTo: r3, Mix: c3},
		},
	}
	// The hot products: 20 drawn from the sale category and the rest of the catalog.
	sale := ix.Categories[ix.SaleCategoryID()-1].Products
	seen := map[int64]bool{}
	for len(s.HotProducts) < 20 {
		var id int64
		if len(s.HotProducts) < 12 {
			id = sale[r.IntN(len(sale))]
		} else {
			id = ix.Items[r.IntN(len(ix.Items))].ID
		}
		if !seen[id] {
			seen[id] = true
			s.HotProducts = append(s.HotProducts, id)
		}
	}
	return s
}

// Calibrate holds the genesis mix at rising rates, one minute per step, to measure what one
// capacity unit can serve. It is for the spike, not for runs.
func Calibrate(seed uint64, ix *catalog.Index, from, to, step float64) *Storyline {
	s := &Storyline{Name: "Calibrate", Seed: seed, GenesisRate: 20, Genesis: genesisMix(), SaleCategory: ix.SaleCategoryID()}
	t := time.Duration(0)
	for rate := from; rate <= to+1e-9; rate += step {
		s.Chapters = append(s.Chapters, Chapter{
			Name: fmt.Sprintf("hold %.0f req/s", rate), Start: t, End: t + time.Minute,
			RateFrom: rate, RateTo: rate, Mix: genesisMix(),
		})
		t += time.Minute
	}
	return s
}

// ByName returns a storyline family by name. "calibrate:FROM:TO:STEP" is the calibration ramp.
func ByName(name string, seed uint64, ix *catalog.Index) *Storyline {
	if rest, ok := strings.CutPrefix(name, "calibrate"); ok {
		from, to, step := 50.0, 500.0, 50.0
		fmt.Sscanf(rest, ":%g:%g:%g", &from, &to, &step)
		return Calibrate(seed, ix, from, to, step)
	}
	switch name {
	default:
		return WordGetsOut(seed, ix)
	}
}
