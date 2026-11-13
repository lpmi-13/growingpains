// Command gp-seed writes the base dataset as a psql script of COPY blocks.
//
//	gp-seed | psql -v ON_ERROR_STOP=1 -d shop
//
// The output is identical on every run, so the database image is reproducible.
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"strings"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
)

func main() {
	products := flag.Int("products", catalog.Default.Products, "number of base products")
	sellers := flag.Int("sellers", catalog.Default.Sellers, "number of sellers")
	flag.Parse()
	spec := catalog.Default
	spec.Products, spec.Sellers = *products, *sellers
	ix := catalog.Build(spec)

	w := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer w.Flush()
	fmt.Fprintln(w, "BEGIN;")

	copyBlock(w, "categories (category_id, slug, name)", func(row func(...any)) {
		for _, c := range ix.Categories {
			row(c.ID, c.Slug, c.Name)
		}
	})
	copyBlock(w, "sellers (seller_id, name, created_at)", func(row func(...any)) {
		for _, s := range ix.Sellers {
			row(s.ID, s.Name, catalog.Epoch)
		}
	})
	copyBlock(w, "products (seller_id, product_id, category_id, title, description, price_cents, image_key, thumbnail_key, created_at, updated_at)", func(row func(...any)) {
		for _, it := range ix.Items {
			p := ix.Full(it)
			var img, thumb any
			if it.HasImage {
				img, thumb = imageKey(it.ID), thumbKey(it.ID)
			}
			row(p.SellerID, p.ID, p.CategoryID, p.Title, p.Description, p.PriceCents, img, thumb, p.CreatedAt, p.CreatedAt)
		}
	})
	copyBlock(w, "variants (seller_id, variant_id, product_id, sku, options, price_cents, stock)", func(row func(...any)) {
		for _, it := range ix.Items {
			for _, v := range ix.Full(it).VariantRows {
				row(it.SellerID, v.ID, it.ID, v.SKU, v.Options, v.PriceCents, v.Stock)
			}
		}
	})
	reviewID := 0
	copyBlock(w, "reviews (seller_id, review_id, product_id, user_id, rating, body, created_at)", func(row func(...any)) {
		for _, it := range ix.Items {
			for _, r := range ix.Full(it).Reviews {
				reviewID++
				row(it.SellerID, reviewID, it.ID, r.UserID, r.Rating, r.Body, r.CreatedAt)
			}
		}
	})
	copyBlock(w, "images (seller_id, image_key, product_id, kind, content_type, bytes, created_at)", func(row func(...any)) {
		for _, it := range ix.Items {
			if !it.HasImage {
				continue
			}
			p := ix.Full(it)
			row(it.SellerID, imageKey(it.ID), it.ID, "original", "image/jpeg", jpegBytes(it, 160, 120), p.CreatedAt)
			row(it.SellerID, thumbKey(it.ID), it.ID, "thumbnail", "image/jpeg", jpegBytes(it, 64, 48), p.CreatedAt)
		}
	})
	copyBlock(w, "followers (seller_id, follower_id)", func(row func(...any)) {
		for _, s := range ix.Sellers {
			for _, f := range ix.Followers(s) {
				row(s.ID, f)
			}
		}
	})
	fmt.Fprintf(w, "SELECT setval(pg_get_serial_sequence('reviews', 'review_id'), %d);\n", reviewID+1)
	fmt.Fprintln(w, "COMMIT;")
	fmt.Fprintln(w, "VACUUM ANALYZE;")
}

func imageKey(id int64) string { return fmt.Sprintf("p%d-0", id) }
func thumbKey(id int64) string { return fmt.Sprintf("p%d-0-t", id) }

// jpegBytes draws a small, deterministic gradient for a base product's photo.
func jpegBytes(it catalog.Item, wd, ht int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, wd, ht))
	h := uint8(it.ID * 37)
	for y := range ht {
		for x := range wd {
			img.Set(x, y, color.RGBA{h + uint8(x), uint8(y * 2), uint8(it.CategoryID * 6), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func copyBlock(w *bufio.Writer, table string, rows func(row func(...any))) {
	fmt.Fprintf(w, "COPY %s FROM stdin;\n", table)
	rows(func(vals ...any) {
		for i, v := range vals {
			if i > 0 {
				w.WriteByte('\t')
			}
			w.WriteString(field(v))
		}
		w.WriteByte('\n')
	})
	fmt.Fprintln(w, `\.`)
}

var escaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

func field(v any) string {
	switch x := v.(type) {
	case nil:
		return `\N`
	case string:
		return escaper.Replace(x)
	case []byte:
		return `\\x` + hex.EncodeToString(x)
	case time.Time:
		return x.UTC().Format("2006-01-02 15:04:05+00")
	default:
		return fmt.Sprint(x)
	}
}
