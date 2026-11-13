package agent

import (
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newTestEdge() *Edge { return NewEdge(prometheus.NewRegistry(), slog.Default()) }

func TestRepeatRatio(t *testing.T) {
	e := newTestEdge()
	now := time.Now()
	// Four reads of one listing and four distinct details.
	for range 4 {
		e.Observe(logLine{Method: "GET", URI: "/products?sort=newest&category=kitchen", Status: 200, Route: "listing"}, now)
	}
	for i := range 4 {
		e.Observe(logLine{Method: "GET", URI: "/products/" + string(rune('1'+i)), Status: 200, Route: "detail"}, now)
	}
	s := e.Shape(now)
	if s["repeat_ratio_listing"] != 0.75 || s["repeat_ratio_detail"] != 0 || s["repeat_ratio_all"] != 3.0/8 || s["repeat_ratio_cacheable"] != 3.0/8 {
		t.Fatalf("repeat ratios %v", s)
	}
	// Query parameter order does not make a new key.
	e.Observe(logLine{Method: "GET", URI: "/products?category=kitchen&sort=newest", Status: 200, Route: "listing"}, now)
	if got := e.Shape(now)["repeat_ratio_listing"]; got != 0.8 {
		t.Fatalf("reordered query counted as a new key: %v", got)
	}
}

func TestWindowForgetsOldBuckets(t *testing.T) {
	e := newTestEdge()
	old := time.Now().Add(-2 * time.Minute)
	e.Observe(logLine{Method: "GET", URI: "/", Status: 200, Route: "home"}, old)
	e.Observe(logLine{Method: "GET", URI: "/", Status: 200, Route: "home"}, old)
	if got := e.Shape(time.Now())["window_requests"]; got != 0 {
		t.Fatalf("requests from two minutes ago still counted: %v", got)
	}
}

func TestSearchSignals(t *testing.T) {
	e := newTestEdge()
	now := time.Now()
	t0 := float64(now.UnixMilli()) / 1000
	lines := []logLine{
		{T: t0, Method: "GET", URI: "/search?q=lam", SID: "a", Route: "search", Results: "3"},
		{T: t0 + 0.2, Method: "GET", URI: "/search?q=lamp", SID: "a", Route: "search", Results: "9"},  // prefix
		{T: t0 + 1, Method: "GET", URI: "/search?q=quitl", SID: "b", Route: "search", Results: "0"},   // zero results
		{T: t0 + 3, Method: "GET", URI: "/search?q=quilt", SID: "b", Route: "search", Results: "12"},  // requery
		{T: t0 + 4, Method: "GET", URI: "/search?q=rug&min_price=500", Route: "search", Results: "4"}, // faceted
	}
	for _, l := range lines {
		e.Observe(l, now)
	}
	s := e.Shape(now)
	for name, want := range map[string]float64{
		"search_prefix_share": 0.2, "search_requery_rate": 0.2, "search_zero_result_rate": 0.2, "search_facet_share": 0.2,
	} {
		if math.Abs(s[name]-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, s[name], want)
		}
	}
}

func TestTopShare(t *testing.T) {
	counts := map[string]uint32{}
	for i := range 200 {
		counts[string(rune(i+1000))] = 1
	}
	counts["hot"] = 100
	counts["warm"] = 50
	// 202 sellers: the top 1% is 3 sellers, holding 100 + 50 + 1 of 350 writes.
	if got := topShare(counts, 350, 0.01); math.Abs(got-151.0/350) > 1e-9 {
		t.Fatalf("top 1%% share %v", got)
	}
}

func TestRouteFallback(t *testing.T) {
	for uri, want := range map[string]string{"/": "home", "/products?category=x": "listing", "/products/12": "detail", "/search?q=a": "search", "/media/p1-0": "media"} {
		if got := routeOf(logLine{Method: "GET", URI: uri}); got != want {
			t.Errorf("%s routed to %s, want %s", uri, got, want)
		}
	}
	if got := routeOf(logLine{Method: "PATCH", URI: "/products/12"}); got != "patch" {
		t.Errorf("PATCH routed to %s", got)
	}
}
