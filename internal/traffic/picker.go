// Package traffic picks what each request asks for: which category, sort, page, and price range
// a listing view uses, and which product a detail view shows. The crowd uses it to send
// requests, and the twin uses the same code to predict how often keys repeat, so the twin's
// shape signals come from the very distributions the crowd draws from.
package traffic

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/storyline"
)

// SortNames are the listing's sort orders, in the order of Mix.SortWeights.
var SortNames = [4]string{"newest", "price_asc", "price_desc", "rating"}

// Picker draws request keys for one storyline.
type Picker struct {
	ix      *catalog.Index
	story   *storyline.Storyline
	popular []int // a seeded permutation, so popular products are not simply the oldest
	zipfs   sync.Map
}

// NewPicker prepares the key distributions for a seed's storyline.
func NewPicker(ix *catalog.Index, story *storyline.Storyline, seed uint64) *Picker {
	return &Picker{ix: ix, story: story, popular: rng.Stream(seed, "popularity", 0).Perm(len(ix.Items))}
}

func (p *Picker) zipf(n int, s float64) *rng.Zipf {
	key := [2]float64{float64(n), math.Round(s*20) / 20}
	if z, ok := p.zipfs.Load(key); ok {
		return z.(*rng.Zipf)
	}
	z := rng.NewZipf(n, key[1])
	p.zipfs.Store(key, z)
	return z
}

// Listing describes one listing view.
type Listing struct {
	Category *catalog.Category
	Sort     int // index into SortNames
	Page     int
	Min, Max int // a price range in cents; zero when unfiltered
	Hot      bool
}

// Path is the request URL.
func (l Listing) Path() string {
	path := fmt.Sprintf("/products?category=%s&sort=%s&page=%d", l.Category.Slug, SortNames[l.Sort], l.Page)
	if l.Max > 0 {
		path += fmt.Sprintf("&min_price=%d&max_price=%d", l.Min, l.Max)
	}
	return path
}

// Key is the cache key the edge counts repetition by.
func (l Listing) Key() string {
	return fmt.Sprintf("l:%d:%d:%d:%d:%d", l.Category.ID, l.Sort, l.Page, l.Min, l.Max)
}

// Listing draws a listing view from the mix.
func (p *Picker) Listing(r *rand.Rand, mix storyline.Mix) Listing {
	if r.Float64() < mix.HotShare {
		cat := p.ix.Categories[p.story.SaleCategory-1]
		// "Top deals": the sale category ranked by rating or price, the expensive aggregate.
		return Listing{Category: cat, Sort: rng.Weighted(r, []float64{0.15, 0.15, 0.0, 0.7}), Page: 1 + rng.Weighted(r, []float64{0.8, 0.2}), Hot: true}
	}
	browse := len(p.ix.Categories) - 1 // the sale category only fills up in a sale
	cat := p.ix.Categories[p.zipf(browse, mix.CategoryZipf).Draw(r)]
	l := Listing{Category: cat, Sort: rng.Weighted(r, mix.SortWeights[:])}
	pages := max(1, (len(cat.Products)+23)/24)
	if r.Float64() < mix.ListingFilter {
		// A price range in whole pounds: shoppers narrow a category in many different ways.
		l.Min = 100 * (5 + r.IntN(150))
		l.Max = l.Min + 100*(10+r.IntN(200))
		pages = max(1, pages/3)
	}
	l.Page = min(rng.Geometric(r, mix.PageMean), mix.PageMax, pages)
	return l
}

// Product draws the product a detail view shows.
func (p *Picker) Product(r *rand.Rand, mix storyline.Mix) (id int64, hot bool) {
	if r.Float64() < mix.HotShare && len(p.story.HotProducts) > 0 {
		return p.story.HotProducts[r.IntN(len(p.story.HotProducts))], true
	}
	rank := p.zipf(len(p.ix.Items), mix.DetailZipf).Draw(r)
	return p.ix.Items[p.popular[rank]].ID, false
}
