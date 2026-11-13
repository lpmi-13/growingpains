// Package catalog generates the shop's base dataset.
//
// The base dataset is fixed across run seeds, because the plan requires that only the traffic
// differs between two runs. The database seeder writes it once at image build time, and the
// crowd regenerates the same lightweight index in memory, so it knows every product ID,
// variant, seller, and category without reading the database.
//
// Product IDs carry their seller: product_id = seller_id*1,000,000 + n. Tenant-scoped tables
// use seller_id in their primary key from genesis, so sharding by seller stays possible later
// without rekeying the schema, and any product URL can be routed to its seller.
package catalog

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lpmi-13/growingpains/internal/rng"
)

// Seed is the base dataset's own seed. It is not a run seed and never changes between runs.
const Seed uint64 = 0x67726f77696e67 // "growing"

// ProductIDBase separates a seller's ID from the product's number within that seller.
const ProductIDBase = 1_000_000

// FirstRunLocal is where products created during a run start numbering within each seller.
// Base products always number below it.
const FirstRunLocal = 500_000

// Epoch is the creation time of the oldest base product. Base timestamps are fixed, so the
// dataset is identical byte for byte on every build.
var Epoch = time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

// Spec sizes the base dataset.
type Spec struct {
	Sellers    int
	Products   int
	Users      int
	ImageShare float64
}

// Default is the genesis catalog: 50,000 products, within the plan's 50,000 to 200,000.
var Default = Spec{Sellers: 2000, Products: 50_000, Users: 100_000, ImageShare: 0.10}

// Category is one browsing category.
type Category struct {
	ID       int
	Slug     string
	Name     string
	Nouns    []string
	Products []int64
}

// Seller is a shop on the marketplace.
type Seller struct {
	ID       int64
	Name     string
	Products []int64
}

// Item is the lightweight record of one base product, enough for the crowd.
type Item struct {
	Index      int
	ID         int64
	SellerID   int64
	CategoryID int
	Variants   int
	HasImage   bool
}

// Index is the whole base catalog, without text or reviews.
type Index struct {
	Spec       Spec
	Categories []*Category
	Sellers    []*Seller
	Items      []Item
	byID       map[int64]int
}

// SaleCategoryID is the category that a flash sale concentrates on.
func (ix *Index) SaleCategoryID() int { return len(ix.Categories) }

// Build generates the index for a spec. It takes tens of milliseconds for the default size.
func Build(spec Spec) *Index {
	ix := &Index{Spec: spec, byID: make(map[int64]int, spec.Products)}
	for i, c := range categoryNouns {
		ix.Categories = append(ix.Categories, &Category{ID: i + 1, Slug: c.Slug, Name: c.Name, Nouns: c.Nouns})
	}
	names := rng.Stream(Seed, "seller-names", 0)
	for s := range spec.Sellers {
		name := fmt.Sprintf("%s %s", sellerFirst[names.IntN(len(sellerFirst))], sellerSecond[names.IntN(len(sellerSecond))])
		ix.Sellers = append(ix.Sellers, &Seller{ID: int64(s + 1), Name: name})
	}
	// Category sizes vary: browsing categories follow a gentle Zipf (exponent 0.3), and the sale category
	// holds about 1% of the catalog.
	browse := len(ix.Categories) - 1
	catWeights := make([]float64, len(ix.Categories))
	for k := range browse {
		catWeights[k] = 1 / math.Pow(float64(k+1), 0.3)
	}
	sum := 0.0
	for _, w := range catWeights[:browse] {
		sum += w
	}
	catWeights[browse] = sum * 0.01
	// A few sellers own many products, and most own a handful.
	sellerZipf := rng.NewZipf(spec.Sellers, 0.9)
	r := rng.Stream(Seed, "assignment", 0)
	local := make([]int, spec.Sellers)
	for i := range spec.Products {
		s := sellerZipf.Draw(r)
		local[s]++
		seller := ix.Sellers[s]
		id := seller.ID*ProductIDBase + int64(local[s])
		cat := ix.Categories[rng.Weighted(r, catWeights)]
		item := Item{
			Index:      i,
			ID:         id,
			SellerID:   seller.ID,
			CategoryID: cat.ID,
			Variants:   1 + rng.Weighted(r, []float64{30, 25, 20, 12, 8, 5}),
			HasImage:   r.Float64() < spec.ImageShare,
		}
		ix.byID[id] = len(ix.Items)
		ix.Items = append(ix.Items, item)
		seller.Products = append(seller.Products, id)
		cat.Products = append(cat.Products, id)
	}
	return ix
}

// Lookup returns a base product by ID.
func (ix *Index) Lookup(id int64) (Item, bool) {
	i, ok := ix.byID[id]
	if !ok {
		return Item{}, false
	}
	return ix.Items[i], true
}

// SellerOf returns the seller encoded in a product ID.
func SellerOf(productID int64) int64 { return productID / ProductIDBase }

// VariantID returns the ID of a product's k-th variant, k from 1.
func VariantID(productID int64, k int) int64 { return productID*10 + int64(k) }

// Vocabulary is every word a product title can contain, for search queries.
func Vocabulary() (nouns, adjs, mats []string) {
	for _, c := range categoryNouns {
		nouns = append(nouns, c.Nouns...)
	}
	return nouns, adjectives, materials
}

// Product is a base product in full.
type Product struct {
	Item
	Title       string
	Description string
	PriceCents  int
	CreatedAt   time.Time
	VariantRows []Variant
	Reviews     []Review
}

// Variant is one purchasable option of a product.
type Variant struct {
	ID         int64
	SKU        string
	Options    string // JSON object
	PriceCents int
	Stock      int
}

// Review is a customer review.
type Review struct {
	UserID    int64
	Rating    int
	Body      string
	CreatedAt time.Time
}

// Full generates the text, variants, and reviews of one base product.
func (ix *Index) Full(item Item) Product {
	r := rng.Stream(Seed, "product", uint64(item.Index))
	cat := ix.Categories[item.CategoryID-1]
	noun := cat.Nouns[r.IntN(len(cat.Nouns))]
	adj := adjectives[r.IntN(len(adjectives))]
	mat := materials[r.IntN(len(materials))]
	col := colours[r.IntN(len(colours))]
	title := titleCase(fmt.Sprintf("%s %s %s", adj, mat, noun))
	if r.IntN(3) == 0 {
		title = titleCase(fmt.Sprintf("%s %s %s in %s", adj, mat, noun, col))
	}
	n := 4 + r.IntN(6)
	parts := make([]string, 0, n+1)
	parts = append(parts, fmt.Sprintf("A %s %s %s.", adj, mat, noun))
	for range n {
		parts = append(parts, fill(sentences[r.IntN(len(sentences))], r, noun, mat, col, adj))
	}
	// Base products span two years, oldest first.
	span := 730 * 24 * time.Hour
	created := Epoch.Add(time.Duration(float64(span) * float64(item.Index) / float64(ix.Spec.Products)))
	price := rng.LogNormalInt(r, 2800, 0.7, 300, 40000)
	p := Product{Item: item, Title: title, Description: strings.Join(parts, " "), PriceCents: price, CreatedAt: created}
	optionSets := [][]string{{"S", "M", "L", "XL", "XS", "XXL"}, colours, {"small", "medium", "large", "set of 2", "set of 4", "set of 6"}}
	set := optionSets[r.IntN(len(optionSets))]
	key := []string{"size", "colour", "pack"}[r.IntN(3)]
	for k := 1; k <= item.Variants; k++ {
		vid := VariantID(item.ID, k)
		p.VariantRows = append(p.VariantRows, Variant{
			ID:         vid,
			SKU:        fmt.Sprintf("SKU-%d", vid),
			Options:    fmt.Sprintf(`{"%s": %q}`, key, set[(k-1)%len(set)]),
			PriceCents: price + (k-1)*r.IntN(400),
			Stock:      2000 + r.IntN(8000),
		})
	}
	reviews := rng.LogNormalInt(r, 6, 1.0, 0, 200)
	for k := range reviews {
		p.Reviews = append(p.Reviews, Review{
			UserID:    int64(1 + r.IntN(ix.Spec.Users)),
			Rating:    1 + rng.Weighted(r, []float64{4, 5, 12, 35, 44}),
			Body:      reviewBodies[r.IntN(len(reviewBodies))],
			CreatedAt: created.Add(time.Duration(k+1) * 37 * time.Hour),
		})
	}
	return p
}

// Followers returns the users who follow a seller. They are notified of new products.
func (ix *Index) Followers(s *Seller) []int64 {
	r := rng.Stream(Seed, "followers", uint64(s.ID))
	n := rng.LogNormalInt(r, 20, 1.2, 0, 2000)
	seen := make(map[int64]bool, n)
	out := make([]int64, 0, n)
	for len(out) < n {
		u := int64(1 + r.IntN(ix.Spec.Users))
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func fill(template string, r interface{ IntN(int) int }, noun, mat, col, adj string) string {
	words := []string{noun, mat, col, adj, materials[r.IntN(len(materials))], colours[r.IntN(len(colours))]}
	var args []any
	for i := 0; i < strings.Count(template, "%s"); i++ {
		args = append(args, words[(i+r.IntN(len(words)))%len(words)])
	}
	return fmt.Sprintf(template, args...)
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if w == "in" || w == "of" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
