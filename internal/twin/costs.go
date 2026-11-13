package twin

import "github.com/lpmi-13/growingpains/internal/traffic"

// Costs are per-request CPU costs, in milliseconds, measured on the local stack on
// 13 November 2026 (docs/calibration.md). API costs are the Python process's CPU, including
// uvicorn's share. Database costs are statement execution time; the database's cgroup CPU is
// about DBOverhead times that.
type Costs struct {
	APIHome, APIListing, APIDetail, APIReread, APISearch                       float64
	APIOrder, APIReview, APICreate, APIImage, APIPatch, APIDelete, APIVariants float64
	APICacheHitSaving                                                          float64 // detail statements skipped on a hit

	DBHome, DBListingNewest, DBListingPrice, DBRatingPerProduct, DBFacets          float64
	DBDetail, DBSearch, DBOrder, DBReview, DBCreate, DBPatch, DBDelete, DBVariants float64
	DBOverhead                                                                     float64

	// The Go read path, for the language move: about a quarter of the Python CPU.
	GoFactor float64
}

// Measured is the calibrated cost model.
var Measured = Costs{
	APIHome: 1.75, APIListing: 2.4, APIDetail: 9.0, APIReread: 6.0, APISearch: 1.9,
	APIOrder: 2.4, APIReview: 1.9, APICreate: 4.3, APIImage: 25, APIPatch: 1.8, APIDelete: 1.7, APIVariants: 2.3,
	APICacheHitSaving: 3.5,

	DBHome: 0.12, DBListingNewest: 0.9, DBListingPrice: 0.55, DBRatingPerProduct: 0.0088, DBFacets: 0.5,
	DBDetail: 1.05, DBSearch: 50, DBOrder: 0.4, DBReview: 0.1, DBCreate: 3.5, DBPatch: 0.2, DBDelete: 0.5, DBVariants: 0.6,
	DBOverhead: 1.7,
	GoFactor:   0.25,
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
