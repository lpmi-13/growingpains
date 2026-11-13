package crowd

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/storyline"
)

// shopper runs one session: a short journey of reads, sometimes ending in an order or review.
func (e *Engine) shopper(ctx context.Context, p *phase, idx uint64) {
	r := rng.Stream(p.seed, "shopper", idx)
	sess := &Session{ID: fmt.Sprintf("s%x-%d", p.seed&0xffff, idx)}
	steps := min(sessionStepCap, rng.Geometric(r, sessionStepMean))
	var lastViewed int64
	for i := range steps {
		if ctx.Err() != nil {
			return
		}
		st := p.state(time.Now())
		mix := st.Mix
		op := weighted(r, mix.Home, mix.Listing, mix.Detail, mix.Search, mix.Order, mix.Review)
		if !e.client.TryAcquire() {
			if i == 0 {
				e.m.Sessions.WithLabelValues("shopper", "refused").Inc()
				return
			}
			e.m.Suppressed.WithLabelValues("shopper", "cap").Inc()
			continue
		}
		if i == 0 {
			e.m.Sessions.WithLabelValues("shopper", "started").Inc()
		}
		switch op {
		case 0:
			e.client.Do(ctx, sess, Request{Journey: "browse", Op: "home", Method: http.MethodGet, Path: "/"})
		case 1:
			e.client.Do(ctx, sess, Request{Journey: "browse", Op: "listing", Method: http.MethodGet, Path: e.listingPath(r, p, mix)})
		case 2:
			lastViewed = e.pickProduct(r, p, mix)
			e.client.Do(ctx, sess, Request{Journey: "view", Op: "detail", Method: http.MethodGet, Path: path("/products/%d", lastViewed)})
		case 3:
			e.search(ctx, r, sess, mix)
		case 4:
			if lastViewed == 0 {
				lastViewed = e.pickProduct(r, p, mix)
			}
			e.order(ctx, r, sess, lastViewed, mix)
		case 5:
			if lastViewed == 0 {
				lastViewed = e.pickProduct(r, p, mix)
			}
			body, _ := json.Marshal(map[string]any{"user_id": 1 + r.IntN(e.ix.Spec.Users), "rating": 1 + weighted(r, 4, 5, 12, 35, 44), "body": "Lovely, as described."})
			e.client.Do(ctx, sess, Request{Journey: "buy", Op: "review", Method: http.MethodPost, Path: path("/products/%d/reviews", lastViewed), Body: body, Idempotency: idemKey(r)})
		}
		e.client.Release()
		think := time.Duration(rng.Exp(r, 0.8) * float64(time.Second))
		select {
		case <-ctx.Done():
			return
		case <-time.After(think):
		}
	}
	e.m.Sessions.WithLabelValues("shopper", "completed").Inc()
}

// anonymous sends one session-less GET of the home page or a listing.
func (e *Engine) anonymous(ctx context.Context, p *phase, idx uint64) {
	r := rng.Stream(p.seed, "anonymous", idx)
	mix := p.state(time.Now()).Mix
	rq := Request{Journey: "browse", Op: "home", Method: http.MethodGet, Path: "/"}
	if r.Float64() < mix.AnonListing {
		rq.Op, rq.Path = "listing", e.listingPath(r, p, mix)
	}
	if !e.client.TryAcquire() {
		e.m.Sessions.WithLabelValues("anonymous", "refused").Inc()
		return
	}
	e.m.Sessions.WithLabelValues("anonymous", "started").Inc()
	e.client.Do(ctx, nil, rq)
	e.client.Release()
	e.m.Sessions.WithLabelValues("anonymous", "completed").Inc()
}

func (e *Engine) listingPath(r *rand.Rand, p *phase, mix storyline.Mix) string {
	return p.picker.Listing(r, mix).Path()
}

func (e *Engine) pickProduct(r *rand.Rand, p *phase, mix storyline.Mix) int64 {
	id, _ := p.picker.Product(r, mix)
	return id
}

func (e *Engine) search(ctx context.Context, r *rand.Rand, sess *Session, mix storyline.Mix) {
	q := e.content.SearchQuery(r, mix.SearchTypo)
	filters := ""
	if r.Float64() < mix.SearchFilter {
		switch r.IntN(2) {
		case 0:
			filters = "&category=" + e.ix.Categories[r.IntN(len(e.ix.Categories)-1)].Slug
		default:
			lo := 500 * (1 + r.IntN(10))
			filters = fmt.Sprintf("&min_price=%d&max_price=%d", lo, lo*3)
		}
	}
	if r.Float64() < mix.SearchKeystroke && len(q) > 3 {
		// Search-as-you-type: every keystroke from the third is a request.
		for n := 3; n < len(q); n++ {
			e.client.Do(ctx, sess, Request{Journey: "search", Op: "search", Method: http.MethodGet, Path: "/search?q=" + url.QueryEscape(q[:n]) + filters})
			time.Sleep(time.Duration(80+r.IntN(120)) * time.Millisecond)
		}
	}
	e.client.Do(ctx, sess, Request{Journey: "search", Op: "search", Method: http.MethodGet, Path: "/search?q=" + url.QueryEscape(q) + filters})
}

func (e *Engine) order(ctx context.Context, r *rand.Rand, sess *Session, product int64, mix storyline.Mix) {
	item, ok := e.ix.Lookup(product)
	if !ok {
		return
	}
	seller := e.ix.Sellers[item.SellerID-1]
	n := min(50, rng.Geometric(r, mix.OrderLinesMean))
	lines := make([]map[string]any, 0, n)
	for k := range n {
		it := item
		if k > 0 && len(seller.Products) > 1 {
			if other, ok := e.ix.Lookup(seller.Products[r.IntN(len(seller.Products))]); ok {
				it = other
			}
		}
		lines = append(lines, map[string]any{"variant_id": catalog.VariantID(it.ID, 1+r.IntN(it.Variants)), "quantity": 1 + r.IntN(2)})
	}
	body, _ := json.Marshal(map[string]any{"seller_id": item.SellerID, "buyer_id": 1 + r.IntN(e.ix.Spec.Users), "lines": lines})
	e.client.Do(ctx, sess, Request{Journey: "buy", Op: "order", Method: http.MethodPost, Path: "/orders", Body: body, Idempotency: idemKey(r)})
}

func idemKey(r *rand.Rand) string {
	return fmt.Sprintf("%016x%016x", r.Uint64(), r.Uint64())
}
