package crowd

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/storyline"
)

// Requests per seller action on average: the write and the re-read that checks it.
const requestsPerSellerAction = 2.0

// After this long, an acknowledged write must be visible to anyone, with or without a token.
const settledAfter = 15 * time.Second

// entry is what the ledger knows about one product: the latest description version the seller
// saw acknowledged, and whether it was deleted.
type entry struct {
	version int
	deleted bool
	acked   time.Time
}

// sellerActor is one seller's tool. Its actions are sequential, so its ledger is never raced.
type sellerActor struct {
	e      *Engine
	p      *phase
	index  int
	seller *catalog.Seller
	hot    []int64 // hot products this seller owns
	weight func(storyline.Mix) float64
	total  *sellerWeights
	sess   *Session
	ledger map[int64]*entry
	live   []int64 // products this actor created that still exist
	r      *rand.Rand
}

// sellerWeights normalises every actor's share of the seller traffic.
type sellerWeights struct {
	mu     sync.Mutex
	actors []*sellerActor
}

func (w *sellerWeights) share(a *sellerActor, mix storyline.Mix) float64 {
	total := 0.0
	for _, other := range w.actors {
		total += other.weight(mix)
	}
	if total == 0 {
		return 0
	}
	return a.weight(mix) / total
}

// sellerActors picks the sellers who act in this phase: the sellers of the hot products, who
// edit them often during a sale, and a seeded sample of everyone else.
func (e *Engine) sellerActors(p *phase) []*sellerActor {
	r := rng.Stream(p.seed, "seller-actors", 0)
	weights := &sellerWeights{}
	bySeller := map[int64]*sellerActor{}
	var actors []*sellerActor
	add := func(s *catalog.Seller) *sellerActor {
		if a, ok := bySeller[s.ID]; ok {
			return a
		}
		a := &sellerActor{
			e: e, p: p, index: len(actors), seller: s, total: weights,
			sess:   &Session{ID: "seller-" + itoa(int(s.ID))},
			ledger: map[int64]*entry{},
			r:      rng.Stream(p.seed, "seller", uint64(s.ID)),
		}
		a.weight = func(storyline.Mix) float64 { return 1 }
		bySeller[s.ID] = a
		actors = append(actors, a)
		return a
	}
	for _, id := range p.story.HotProducts {
		a := add(e.ix.Sellers[catalog.SellerOf(id)-1])
		a.hot = append(a.hot, id)
		a.weight = func(m storyline.Mix) float64 { return math.Max(1, m.HotSellerMul) }
	}
	for len(actors) < len(p.story.HotProducts)+e.cfg.Sellers {
		add(e.ix.Sellers[r.IntN(len(e.ix.Sellers))])
	}
	weights.actors = actors
	return actors
}

func (a *sellerActor) rate(at time.Time) float64 {
	st := a.p.state(at)
	return st.Rate * st.Mix.SellerShare / requestsPerSellerAction * a.total.share(a, st.Mix)
}

func (a *sellerActor) run(ctx context.Context) {
	arrivals := rng.Stream(a.p.seed, "seller-arrivals", uint64(a.seller.ID))
	next := time.Now()
	if !a.p.start.IsZero() && a.p.start.After(next) {
		next = a.p.start
	}
	gap := func(at time.Time) time.Duration {
		return time.Duration(arrivals.ExpFloat64() / math.Max(a.rate(at), 0.001) * float64(time.Second))
	}
	next = next.Add(gap(next))
	chapter := a.p.state(next).Chapter
	for {
		if !a.p.start.IsZero() && !next.Before(a.p.start.Add(a.p.story.Duration())) {
			return
		}
		wait := time.Until(next)
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !a.p.start.IsZero() && !time.Now().Before(a.p.start.Add(a.p.story.Duration())) {
			return
		}
		a.e.m.Sessions.WithLabelValues("seller", "scheduled").Inc()
		now := time.Now()
		st := a.p.state(now)
		// The end of each chapter is a ledger checkpoint: every settled write must still be there.
		if st.Chapter != chapter {
			chapter = st.Chapter
			a.audit(ctx)
		}
		if a.e.client.TryAcquire() {
			a.e.m.Sessions.WithLabelValues("seller", "started").Inc()
			a.act(ctx, st.Mix)
			a.e.client.Release()
			a.e.m.Sessions.WithLabelValues("seller", "completed").Inc()
		} else {
			a.e.m.Sessions.WithLabelValues("seller", "refused").Inc()
		}
		// Actions that came due while this one ran were never sent; count them.
		next = next.Add(gap(next))
		for next.Before(time.Now()) {
			a.e.m.Suppressed.WithLabelValues("seller", "busy").Inc()
			next = next.Add(gap(next))
		}
	}
}

func (a *sellerActor) act(ctx context.Context, mix storyline.Mix) {
	r := a.r
	choice := weighted(r, mix.Create, mix.Patch, mix.Variants, mix.Delete, mix.Reread)
	if (choice == 2 || choice == 3) && len(a.live) == 0 {
		choice = 0
	}
	switch choice {
	case 0:
		a.create(ctx, r, mix)
	case 1:
		a.patch(ctx, r, mix)
	case 2:
		a.variants(ctx, r)
	case 3:
		a.remove(ctx, r)
	default:
		a.reread(ctx, r)
	}
}

func (a *sellerActor) create(ctx context.Context, r *rand.Rand, mix storyline.Mix) {
	c := a.e.content
	price := 500 + r.IntN(9000)
	variants := make([]map[string]any, 1+r.IntN(4))
	for i := range variants {
		variants[i] = map[string]any{"options": map[string]string{"size": []string{"S", "M", "L", "XL"}[i]}, "price_cents": price + i*200, "stock": 50 + r.IntN(500)}
	}
	body := map[string]any{
		"seller_id": a.seller.ID, "category_id": 1 + r.IntN(len(a.e.ix.Categories)-1), "title": c.Title(r),
		"description": c.Description(r, 1), "price_cents": price, "variants": variants,
	}
	if r.Float64() < mix.ImageShare {
		body["image"] = c.Image(r)
	}
	data, _ := json.Marshal(body)
	res := a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "create", Method: http.MethodPost, Path: "/products", Body: data, JSON: true, Idempotency: idemKey(r)})
	var out struct {
		ID      int64 `json:"id"`
		Version int   `json:"description_version"`
	}
	if res.Status != http.StatusCreated || json.Unmarshal(res.Body, &out) != nil || out.ID == 0 {
		return
	}
	a.ledger[out.ID] = &entry{version: out.Version, acked: time.Now()}
	a.live = append(a.live, out.ID)
	a.check(ctx, out.ID, "read_your_writes")
}

// target picks the product to edit: during a sale, mostly this seller's hot products.
func (a *sellerActor) target(r *rand.Rand, mix storyline.Mix) int64 {
	if len(a.hot) > 0 && mix.HotShare > 0 && r.Float64() < 0.8 {
		return a.hot[r.IntN(len(a.hot))]
	}
	if len(a.live) > 0 && r.IntN(2) == 0 {
		return a.live[r.IntN(len(a.live))]
	}
	return a.seller.Products[r.IntN(len(a.seller.Products))]
}

func (a *sellerActor) patch(ctx context.Context, r *rand.Rand, mix storyline.Mix) {
	id := a.target(r, mix)
	known := a.ledger[id]
	next := 2
	if known != nil {
		next = known.version + 1
	}
	body, _ := json.Marshal(map[string]any{"description": a.e.content.Description(r, next), "price_cents": 500 + r.IntN(9000)})
	res := a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "patch", Method: http.MethodPatch, Path: path("/products/%d", id), Body: body, JSON: true, Idempotency: idemKey(r)})
	var out struct {
		Version int `json:"description_version"`
	}
	if res.Status != http.StatusOK || json.Unmarshal(res.Body, &out) != nil {
		return
	}
	a.ledger[id] = &entry{version: out.Version, acked: time.Now()}
	a.check(ctx, id, "read_your_writes")
}

func (a *sellerActor) variants(ctx context.Context, r *rand.Rand) {
	id := a.live[r.IntN(len(a.live))]
	vs := make([]map[string]any, 1+r.IntN(6))
	for i := range vs {
		vs[i] = map[string]any{"options": map[string]any{"pack": i + 1}, "price_cents": 400 + r.IntN(6000), "stock": r.IntN(400)}
	}
	body, _ := json.Marshal(map[string]any{"variants": vs})
	res := a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "variants", Method: http.MethodPut, Path: path("/products/%d/variants", id), Body: body, JSON: true, Idempotency: idemKey(r)})
	if res.Status == http.StatusOK {
		if e := a.ledger[id]; e != nil {
			e.acked = time.Now()
		}
	}
}

func (a *sellerActor) remove(ctx context.Context, r *rand.Rand) {
	i := r.IntN(len(a.live))
	id := a.live[i]
	res := a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "delete", Method: http.MethodDelete, Path: path("/products/%d", id), JSON: true, Idempotency: idemKey(r)})
	if res.Status != http.StatusOK && res.Status != http.StatusNotFound {
		return
	}
	a.live = append(a.live[:i], a.live[i+1:]...)
	a.ledger[id] = &entry{deleted: true, acked: time.Now()}
	a.check(ctx, id, "read_your_writes")
}

// reread looks at one of the seller's own products, as a seller checking their shop would.
func (a *sellerActor) reread(ctx context.Context, r *rand.Rand) {
	if len(a.ledger) == 0 {
		id := a.seller.Products[r.IntN(len(a.seller.Products))]
		a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "reread", Method: http.MethodGet, Path: path("/products/%d", id), JSON: true})
		return
	}
	ids := a.ledgerIDs()
	id := ids[r.IntN(len(ids))]
	kind := ""
	if time.Since(a.ledger[id].acked) > settledAfter {
		kind = "missing"
	}
	a.check(ctx, id, kind)
}

func (a *sellerActor) ledgerIDs() []int64 {
	ids := make([]int64, 0, len(a.ledger))
	for id := range a.ledger {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// check reads a product back and compares it with the ledger. kind names the violation it
// would be; an empty kind reads without judging, because the write has not settled yet.
func (a *sellerActor) check(ctx context.Context, id int64, kind string) {
	res := a.e.client.Do(ctx, a.sess, Request{Journey: "sell", Op: "reread", Method: http.MethodGet, Path: path("/products/%d", id), JSON: true})
	a.judge(res, id, kind)
}

func (a *sellerActor) judge(res Result, id int64, kind string) {
	want := a.ledger[id]
	if kind == "" || want == nil || res.Failed() || res.Status == 0 {
		return
	}
	switch {
	case want.deleted && res.Status == http.StatusOK:
		a.e.m.Correctness.WithLabelValues(kind).Inc()
	case !want.deleted && res.Status == http.StatusNotFound:
		a.e.m.Correctness.WithLabelValues(kind).Inc()
	case !want.deleted && res.Status == http.StatusOK:
		var got struct {
			Product struct {
				Version int `json:"description_version"`
			} `json:"product"`
		}
		if json.Unmarshal(res.Body, &got) == nil && got.Product.Version < want.version {
			if kind == "missing" {
				kind = "stale"
			}
			a.e.m.Correctness.WithLabelValues(kind).Inc()
		}
	}
}

// audit checks every settled write in the ledger from a fresh session with no token. It is
// the end-of-chapter check that no acknowledged write was lost.
func (a *sellerActor) audit(ctx context.Context) {
	fresh := &Session{}
	ids := a.ledgerIDs()
	if len(ids) > 40 {
		ids = ids[len(ids)-40:]
	}
	for _, id := range ids {
		if time.Since(a.ledger[id].acked) <= settledAfter {
			continue
		}
		res := a.e.client.Send(ctx, fresh, Request{Journey: "audit", Op: "audit", Method: http.MethodGet, Path: path("/products/%d", id), JSON: true})
		if res.Outcome == "refused" {
			continue
		}
		a.judgeAudit(res, id)
	}
}

func (a *sellerActor) judgeAudit(res Result, id int64) {
	want := a.ledger[id]
	if want == nil || res.Failed() || res.Status == 0 {
		return
	}
	lost := (want.deleted && res.Status == http.StatusOK) || (!want.deleted && res.Status == http.StatusNotFound)
	if !lost && !want.deleted && res.Status == http.StatusOK {
		var got struct {
			Product struct {
				Version int `json:"description_version"`
			} `json:"product"`
		}
		lost = json.Unmarshal(res.Body, &got) == nil && got.Product.Version < want.version
	}
	if lost {
		a.e.m.Correctness.WithLabelValues("lost").Inc()
	}
}
