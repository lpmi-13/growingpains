package twin

import "github.com/lpmi-13/growingpains/internal/traffic"

// Costs are per-request CPU costs, in milliseconds (docs/calibration.md).
//
// API costs are the Python process's CPU, including uvicorn's share, measured on the local stack
// on 13 November 2026. The twin counts each API instance as one core of the local stack. A
// playground vCPU runs the API about 2.4 times faster, so the playground's genesis process is
// capped at 40% of a core to stay one such unit; `gp-twin -api-cost 0.43` shows what a full
// playground core would do.
//
// Database costs are statement execution time on a playground node, from pg_stat_statements in
// the full storylines of 15 November 2026, except indexed search: 10 ms is a conservative
// local estimate pending a fresh playground run (the old sequential scan was 50 ms). Thus
// database cores are playground cores. The
// database's cgroup CPU is DBBaseCores, which it uses with no requests, plus DBOverhead times
// statement time.
type Costs struct {
	APIHome, APIListing, APIDetail, APIReread, APISearch                       float64
	APIOrder, APIReview, APICreate, APIImage, APIPatch, APIDelete, APIVariants float64
	APICacheHitSaving                                                          float64 // detail statements skipped on a hit

	DBHome, DBListingNewest, DBListingPrice, DBRatingPerProduct, DBFacets          float64
	DBDetail, DBSearch, DBOrder, DBReview, DBCreate, DBPatch, DBDelete, DBVariants float64
	DBOverhead, DBBaseCores                                                        float64

	// The Go read path, for the language move: about a quarter of the Python CPU.
	GoFactor float64
}

// Measured is the calibrated cost model.
var Measured = Costs{
	APIHome: 1.75, APIListing: 2.4, APIDetail: 9.0, APIReread: 6.0, APISearch: 1.9,
	APIOrder: 2.4, APIReview: 1.9, APICreate: 4.3, APIImage: 25, APIPatch: 1.8, APIDelete: 1.7, APIVariants: 2.3,
	APICacheHitSaving: 3.5,

	DBHome: 0.06, DBListingNewest: 0.85, DBListingPrice: 0.29, DBRatingPerProduct: 0.0044, DBFacets: 0.5,
	DBDetail: 0.45, DBSearch: 10, DBOrder: 0.2, DBReview: 0.05, DBCreate: 1.75, DBPatch: 0.1, DBDelete: 0.25, DBVariants: 0.3,
	DBOverhead: 1.1, DBBaseCores: 0.06,
	GoFactor: 0.25,
}

// ScaleAPI returns the costs with every API cost multiplied by f: a host whose Python process
// runs faster or slower than the local stack's, such as a playground VM (docs/calibration.md).
func (c Costs) ScaleAPI(f float64) Costs {
	for _, p := range []*float64{&c.APIHome, &c.APIListing, &c.APIDetail, &c.APIReread, &c.APISearch,
		&c.APIOrder, &c.APIReview, &c.APICreate, &c.APIImage, &c.APIPatch, &c.APIDelete, &c.APIVariants,
		&c.APICacheHitSaving} {
		*p *= f
	}
	return c
}

// ScaleDB returns the costs with every statement's database time multiplied by f, so the
// database's CPU scales by f too: a host whose PostgreSQL runs faster or slower than the local
// stack's (docs/calibration.md).
func (c Costs) ScaleDB(f float64) Costs {
	for _, p := range []*float64{&c.DBHome, &c.DBListingNewest, &c.DBListingPrice, &c.DBRatingPerProduct, &c.DBFacets,
		&c.DBDetail, &c.DBSearch, &c.DBOrder, &c.DBReview, &c.DBCreate, &c.DBPatch, &c.DBDelete, &c.DBVariants} {
		*p *= f
	}
	return c
}

// ListingDB is one listing view's database execution time. Sorting by rating aggregates the
// reviews of every product in range, so it grows with the category; the other sorts page first.
func (c Costs) ListingDB(l traffic.Listing) float64 {
	cost := c.DBFacets
	switch traffic.SortNames[l.Sort] {
	case "rating":
		n := float64(len(l.Category.Products))
		if l.Max > 0 {
			n *= 0.45 // a price range keeps about this share of a category
		}
		cost += c.DBRatingPerProduct * n
	case "newest":
		cost += c.DBListingNewest
	default:
		cost += c.DBListingPrice
	}
	return cost
}
