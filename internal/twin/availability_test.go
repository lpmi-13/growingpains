package twin

import (
	"math"
	"testing"

	"github.com/lpmi-13/growingpains/internal/catalog"
)

func TestSearchTimeoutUnderQuotaMatchesMeasuredRisk(t *testing.T) {
	// Conditional risks inferred from the 17 November live search minutes. The fixed-rate
	// benchmark independently put search p99 near the two-second deadline at 80% busy.
	for _, tc := range []struct {
		used, cap, want float64
	}{
		{0.70 * 0.65, 0.65, 0},
		{0.76 * 0.65, 0.65, 0.017},
		{0.90 * 0.65, 0.65, 0.20},
		{0.54, 0.87, 0}, // the proposed replica headroom
		{0.54, 2, 0},    // the measured uncapped replica
	} {
		got := searchTimeoutUnderQuota(tc.used, tc.cap, 1)
		if math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("searchTimeoutUnderQuota(%g, %g) = %g, want %g", tc.used, tc.cap, got, tc.want)
		}
	}
	if got := searchTimeoutUnderQuota(0.54, 0.65, 1); got < 0.05 || got > 0.07 {
		t.Errorf("measured 0.54-core plateau under a 0.65-core quota: risk = %g, want about 6%%", got)
	}
	if got := searchTimeoutUnderQuota(0.9*0.65, 0.65, 0.2); got >= 0.001 {
		t.Errorf("5x faster search should cut the two-second tail; got %g", got)
	}
}

func TestReservedHeadroomProtectsUnindexedSearch(t *testing.T) {
	ix := catalog.Build(catalog.Default)
	opts := DefaultOptions
	opts.Costs.DBSearch = unindexedSearchMS // the observed, pre-index playground run
	res, err := Run(ix, 1003, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.WorstByJourney) != len(journeys) {
		t.Fatalf("got %d journeys, want %d", len(res.WorstByJourney), len(journeys))
	}
	for _, journey := range journeys {
		if got := res.WorstByJourney[journey]; got < 0 || got > 1 {
			t.Errorf("%s availability = %g", journey, got)
		}
	}
	if res.WorstJourneyAvailability > res.WorstAvailability {
		t.Errorf("worst journey %g exceeds aggregate %g", res.WorstJourneyAvailability, res.WorstAvailability)
	}
	if res.WorstByJourney["search"] < 0.995 || res.WorstByJourney["browse"] < 0.995 {
		t.Errorf("the replica's reserved headroom should protect both read journeys; got %v", res.WorstByJourney)
	}
}

func TestRepeatPressureKeepsCacheEligibleAfterHeadroom(t *testing.T) {
	ix := catalog.Build(catalog.Default)
	opts := DefaultOptions
	opts.Costs = opts.Costs.ScaleAPI(1.075)
	opts.Costs.DBSearch = unindexedSearchMS // the original search query
	base, err := Run(ix, 1003, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.ReplicaCores = 0.87
	headroom, err := Run(ix, 1003, opts)
	if err != nil {
		t.Fatal(err)
	}
	cacheDecision := func(r Result) float64 {
		for _, m := range r.Moves {
			if m.Move == "cache" {
				return m.Decided.Seconds()
			}
		}
		return 0
	}
	if cacheDecision(base) < 10*60 || cacheDecision(base) > 12*60 ||
		math.Abs(cacheDecision(headroom)-cacheDecision(base)) > 60 {
		t.Errorf("repeat pressure should still choose the cache near the sale: %v, %v", cacheDecision(base), cacheDecision(headroom))
	}
}

func TestIndexedSearchMVPPathAndAvailability(t *testing.T) {
	ix := catalog.Build(catalog.Default)
	opts := DefaultOptions
	opts.Costs = opts.Costs.ScaleAPI(1.075)
	res, err := Run(ix, 1003, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := [...]string{"autoscaled-fleet", "read-replica", "cache"}
	path := res.Path()
	if len(path) != len(want) {
		t.Fatalf("path = %v, want %v", path, want)
	}
	for i, move := range path {
		if move != want[i] {
			t.Fatalf("path = %v, want %v", path, want)
		}
	}
	if why := res.Validate(); len(why) != 0 {
		t.Errorf("indexed search should meet the MVP gates in seed 1003: %v", why)
	}
}
