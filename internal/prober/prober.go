// Package prober sends low, constant synthetic checks: one canary per journey, plus freshness
// probes that edit a canary product and poll for the change from the same session and from a
// new one. Its rate never changes, so its SLIs stay comparable across the whole run.
package prober

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lpmi-13/growingpains/internal/crowd"
)

// Prober owns a canary seller and its products.
type Prober struct {
	client *crowd.Client
	log    *slog.Logger

	staleness   *prometheus.HistogramVec
	correctness *prometheus.CounterVec

	seller    int64
	canary    int64 // edited by the detail freshness probe
	buyable   int64 // ordered by the buy journey
	listingID int64 // the newest canary in the listing probe's category
	version   int
	sess      *crowd.Session
	seq       int
}

// New registers the prober's metrics. Requests go through a crowd client and are counted in the
// same series as the crowd's, under the job "prober" and the phase "probe", so the judge's SLIs
// include them but the run's error budget does not.
func New(base string, reg prometheus.Registerer, log *slog.Logger) *Prober {
	m := crowd.NewMetrics(reg)
	p := &Prober{log: log, sess: &crowd.Session{ID: "prober"}}
	p.client = crowd.NewClient(base, 16, m)
	p.client.SetPhase("probe")
	p.staleness = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "gp_probe_staleness_seconds", Help: "Time until an acknowledged edit is visible",
		Buckets: []float64{0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 1.5, 2, 3, 5, 10, 15, 20, 30},
	}, []string{"view", "session"})
	p.correctness = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gp_probe_correctness_errors_total", Help: "Probe reads that contradicted an acknowledged write",
	}, []string{"kind"})
	reg.MustRegister(p.staleness, p.correctness)
	p.correctness.WithLabelValues("read_your_writes")
	return p
}

func (p *Prober) send(ctx context.Context, s *crowd.Session, rq crowd.Request) crowd.Result {
	return p.client.Send(ctx, s, rq)
}

func (p *Prober) post(ctx context.Context, journey, op, path string, body any) (crowd.Result, map[string]any) {
	data, _ := json.Marshal(body)
	p.seq++
	res := p.send(ctx, p.sess, crowd.Request{Journey: journey, Op: op, Method: http.MethodPost, Path: path, Body: data, JSON: true,
		Idempotency: fmt.Sprintf("probe-%d-%d", time.Now().UnixNano(), p.seq)})
	var out map[string]any
	json.Unmarshal(res.Body, &out)
	return res, out
}

// Setup creates the canary seller and products, retrying until the shop answers.
func (p *Prober) Setup(ctx context.Context) error {
	for ctx.Err() == nil {
		res, out := p.post(ctx, "setup", "seller", "/sellers", map[string]any{"name": "Canary Supply Co."})
		if res.Status == http.StatusCreated {
			p.seller = int64(out["seller_id"].(float64))
			break
		}
		time.Sleep(2 * time.Second)
	}
	make := func(title string, category int, stock int) int64 {
		for ctx.Err() == nil {
			res, out := p.post(ctx, "setup", "create", "/products", map[string]any{
				"seller_id": p.seller, "category_id": category, "title": title, "description": "Canary (edit 1)",
				"price_cents": 100, "variants": []map[string]any{{"options": map[string]string{"size": "one"}, "price_cents": 100, "stock": stock}},
			})
			if res.Status == http.StatusCreated {
				return int64(out["id"].(float64))
			}
			time.Sleep(2 * time.Second)
		}
		return 0
	}
	p.canary = make("Canary Freshness Lamp", 3, 10)
	p.buyable = make("Canary Order Mug", 2, 100_000_000)
	p.version = 1
	p.log.Info("prober ready", "seller", p.seller, "canary", p.canary, "buyable", p.buyable)
	return ctx.Err()
}

// Run probes until ctx ends.
func (p *Prober) Run(ctx context.Context) {
	journeys := time.NewTicker(2 * time.Second)
	fresh := time.NewTicker(15 * time.Second)
	listing := time.NewTicker(60 * time.Second)
	defer journeys.Stop()
	defer fresh.Stop()
	defer listing.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-journeys.C:
			p.journey(ctx, n%5)
			n++
		case <-fresh.C:
			p.detailFreshness(ctx)
		case <-listing.C:
			p.listingFreshness(ctx)
		}
	}
}

func (p *Prober) journey(ctx context.Context, which int) {
	get := func(journey, op, path string) {
		p.send(ctx, &crowd.Session{ID: "prober-reader"}, crowd.Request{Journey: journey, Op: op, Method: http.MethodGet, Path: path})
	}
	switch which {
	case 0:
		get("browse", "home", "/")
		get("browse", "listing", "/products?category=kitchen&sort=newest&page=1")
	case 1:
		get("view", "detail", "/products/1000001")
	case 2:
		get("search", "search", "/search?q=lamp")
	case 3:
		p.post(ctx, "sell", "review", fmt.Sprintf("/products/%d/reviews", p.canary), map[string]any{"user_id": 1, "rating": 5, "body": "canary"})
	case 4:
		p.post(ctx, "buy", "order", "/orders", map[string]any{
			"seller_id": p.seller, "buyer_id": 1, "lines": []map[string]any{{"variant_id": p.buyable*10 + 1, "quantity": 1}},
		})
	}
}

func versionOf(res crowd.Result) int {
	var got struct {
		Product struct {
			Version int `json:"description_version"`
		} `json:"product"`
	}
	if json.Unmarshal(res.Body, &got) != nil {
		return 0
	}
	return got.Product.Version
}

// detailFreshness edits the canary, then reads it back from the same session (which must see
// it at once) and from a new session (which records how long it took).
func (p *Prober) detailFreshness(ctx context.Context) {
	data, _ := json.Marshal(map[string]any{"description": fmt.Sprintf("Canary (edit %d)", p.version+1)})
	p.seq++
	res := p.send(ctx, p.sess, crowd.Request{Journey: "sell", Op: "patch", Method: http.MethodPatch, Path: fmt.Sprintf("/products/%d", p.canary),
		Body: data, JSON: true, Idempotency: fmt.Sprintf("probe-edit-%d-%d", time.Now().UnixNano(), p.seq)})
	if res.Status != http.StatusOK {
		return
	}
	var out struct {
		Version int `json:"description_version"`
	}
	json.Unmarshal(res.Body, &out)
	p.version = out.Version
	acked := time.Now()
	path := fmt.Sprintf("/products/%d", p.canary)

	own := p.send(ctx, p.sess, crowd.Request{Journey: "sell", Op: "reread", Method: http.MethodGet, Path: path, JSON: true})
	if own.Status == http.StatusOK && versionOf(own) < p.version {
		p.correctness.WithLabelValues("read_your_writes").Inc()
	}
	for time.Since(acked) < 30*time.Second && ctx.Err() == nil {
		fresh := p.send(ctx, &crowd.Session{}, crowd.Request{Journey: "probe", Op: "fresh", Method: http.MethodGet, Path: path, JSON: true})
		if fresh.Status == http.StatusOK && versionOf(fresh) >= p.version {
			p.staleness.WithLabelValues("detail", "new").Observe(time.Since(acked).Seconds())
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.staleness.WithLabelValues("detail", "new").Observe(30)
}

// listingFreshness lists a new canary product and polls the category's newest page for it.
func (p *Prober) listingFreshness(ctx context.Context) {
	const category = "vintage"
	res, out := p.post(ctx, "sell", "create", "/products", map[string]any{
		"seller_id": p.seller, "category_id": 37, "title": "Canary Listing Clock", "description": "Canary",
		"price_cents": 100, "variants": []map[string]any{{"price_cents": 100, "stock": 1}},
	})
	if res.Status != http.StatusCreated {
		return
	}
	id := int64(out["id"].(float64))
	acked := time.Now()
	needle := fmt.Sprintf(`"id":%d`, id)
	for time.Since(acked) < 30*time.Second && ctx.Err() == nil {
		page := p.send(ctx, &crowd.Session{}, crowd.Request{Journey: "probe", Op: "fresh_listing", Method: http.MethodGet,
			Path: "/products?category=" + category + "&sort=newest&page=1", JSON: true})
		if page.Status == http.StatusOK && strings.Contains(string(page.Body), needle) {
			p.staleness.WithLabelValues("listing", "new").Observe(time.Since(acked).Seconds())
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if time.Since(acked) >= 30*time.Second {
		p.staleness.WithLabelValues("listing", "new").Observe(30)
	}
	if p.listingID != 0 {
		p.seq++
		p.send(ctx, p.sess, crowd.Request{Journey: "sell", Op: "delete", Method: http.MethodDelete, Path: fmt.Sprintf("/products/%d", p.listingID),
			JSON: true, Idempotency: fmt.Sprintf("probe-del-%d-%d", time.Now().UnixNano(), p.seq)})
	}
	p.listingID = id
}
