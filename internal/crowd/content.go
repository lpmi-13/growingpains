package crowd

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"strings"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
)

// Content makes the text and images that sellers upload, and the words shoppers search for.
type Content struct {
	nouns, adjs, mats []string
	images            []string // base64 JPEGs, made once at startup
}

// NewContent prepares the vocabulary and a few upload-sized photos.
func NewContent() *Content {
	c := &Content{}
	c.nouns, c.adjs, c.mats = catalog.Vocabulary()
	for i := range 4 {
		c.images = append(c.images, photo(640, 480, i))
	}
	return c
}

func photo(w, h, variant int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			// Noise-like texture, so the JPEG is a realistic size rather than a flat colour.
			v := uint8((x*7 + y*13 + (x*y)%251 + variant*40) & 0xff)
			img.Set(x, y, color.RGBA{v, uint8(y / 2), uint8(x / 3), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func pick(r *rand.Rand, words []string) string { return words[r.IntN(len(words))] }

// Title is a new product's name.
func (c *Content) Title(r *rand.Rand) string {
	return fmt.Sprintf("%s %s %s", titleWord(pick(r, c.adjs)), titleWord(pick(r, c.mats)), pick(r, c.nouns))
}

// Description is a product description of a few sentences, with a version marker the ledger
// can check.
func (c *Content) Description(r *rand.Rand, version int) string {
	var b strings.Builder
	n := 3 + r.IntN(5)
	for i := range n {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "A %s %s %s, finished by hand.", pick(r, c.adjs), pick(r, c.mats), pick(r, c.nouns))
	}
	fmt.Fprintf(&b, " (edit %d)", version)
	return b.String()
}

// Image returns one of the prepared photos.
func (c *Content) Image(r *rand.Rand) string { return c.images[r.IntN(len(c.images))] }

// SearchQuery returns a query of one or two catalog words, with a typo at the given rate.
func (c *Content) SearchQuery(r *rand.Rand, typo float64) string {
	words := []string{pick(r, c.nouns)}
	if r.IntN(3) == 0 {
		words = append([]string{pick(r, c.mats)}, words...)
	}
	q := strings.Join(words, " ")
	if r.Float64() < typo && len(q) > 4 {
		i := 1 + r.IntN(len(q)-2)
		switch r.IntN(3) {
		case 0: // swap two letters
			b := []byte(q)
			b[i], b[i+1] = b[i+1], b[i]
			q = string(b)
		case 1: // drop a letter
			q = q[:i] + q[i+1:]
		default: // double a letter
			q = q[:i] + q[i:i+1] + q[i:]
		}
	}
	return q
}

func titleWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// stream is a convenience for per-actor sub-streams.
func stream(seed uint64, name string, index uint64) *rand.Rand { return rng.Stream(seed, name, index) }
