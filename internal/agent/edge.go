// Package agent is the per-node exporter: cgroup CPU against quotas, PostgreSQL statistics,
// and, on the edge node, traffic-shape signals computed from nginx's request log.
package agent

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// logLine is one request as nginx logs it (see deploy/nginx/shop.conf).
type logLine struct {
	T       float64 `json:"t"`
	Method  string  `json:"m"`
	URI     string  `json:"u"`
	Status  int     `json:"s"`
	RT      float64 `json:"rt"`
	SID     string  `json:"sid"`
	Key     string  `json:"key"`
	Tok     string  `json:"tok"`
	Route   string  `json:"route"`
	Seller  string  `json:"seller"`
	Results string  `json:"results"`
	Role    string  `json:"role"`
	Up      string  `json:"up"`
}

// The window over which shape signals are computed, in buckets.
const (
	bucketSpan = 10 * time.Second
	buckets    = 6
)

type bucket struct {
	start      time.Time
	keys       map[string]uint32 // cacheable read keys
	keysRoute  map[string]string
	clients    map[string]uint32
	requests   uint32
	sellers    map[string]uint32
	writes     uint32
	searches   uint32
	prefixes   uint32
	zero       uint32
	requeries  uint32
	faceted    uint32
	reads      uint32
	tokenReads uint32
}

func newBucket(start time.Time) *bucket {
	return &bucket{start: start, keys: map[string]uint32{}, keysRoute: map[string]string{}, clients: map[string]uint32{}, sellers: map[string]uint32{}}
}

type lastSearch struct {
	q string
	t float64
}

// Edge consumes nginx's log stream and exports traffic-shape signals.
type Edge struct {
	log *slog.Logger
	mu  sync.Mutex
	win []*bucket
	ses map[string]lastSearch

	requests *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	upstream *prometheus.CounterVec
	shape    *prometheus.GaugeVec
}

// NewEdge registers the edge metrics.
func NewEdge(reg prometheus.Registerer, log *slog.Logger) *Edge {
	e := &Edge{
		log: log,
		ses: map[string]lastSearch{},
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_edge_requests_total", Help: "Requests through nginx by route, method, status class, and who sent them",
		}, []string{"route", "method", "code", "who"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gp_edge_request_duration_seconds", Help: "Server-side time at nginx",
			Buckets: []float64{0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		}, []string{"route"}),
		upstream: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_edge_upstream_requests_total", Help: "Requests by the upstream server that answered, and its status class",
		}, []string{"upstream", "code"}),
		shape: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gp_edge_shape", Help: "Traffic-shape signals over the last 60 s",
		}, []string{"signal"}),
	}
	reg.MustRegister(e.requests, e.latency, e.upstream, e.shape)
	return e
}

// Listen reads nginx's syslog datagrams until the connection closes.
func (e *Edge) Listen(addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	go e.publishLoop()
	buf := make([]byte, 64*1024)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return err
		}
		if i := bytes.IndexByte(buf[:n], '{'); i >= 0 {
			var line logLine
			if json.Unmarshal(buf[i:n], &line) == nil {
				e.Observe(line, time.Now())
			}
		}
	}
}

// routeOf names a request's route, from the API's header or, when nginx answered alone, its URI.
func routeOf(l logLine) string {
	if l.Route != "" {
		return l.Route
	}
	p := l.URI
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	switch {
	case p == "/":
		return "home"
	case p == "/products" && l.Method == "GET":
		return "listing"
	case p == "/products" && l.Method == "POST":
		return "create"
	case strings.HasPrefix(p, "/products/") && strings.HasSuffix(p, "/reviews"):
		return "review"
	case strings.HasPrefix(p, "/products/") && strings.HasSuffix(p, "/variants"):
		return "variants"
	case strings.HasPrefix(p, "/products/"):
		return map[string]string{"GET": "detail", "PATCH": "patch", "DELETE": "delete"}[l.Method]
	case p == "/search":
		return "search"
	case p == "/orders":
		return "order"
	case strings.HasPrefix(p, "/media/"):
		return "media"
	case strings.HasPrefix(p, "/sellers"):
		return "seller"
	}
	return "other"
}

func cacheKey(route, uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return route + uri
	}
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(u.Path)
	for _, k := range keys {
		b.WriteString("&" + k + "=" + q.Get(k))
	}
	return b.String()
}

// Observe adds one request to the counters and the current window.
func (e *Edge) Observe(l logLine, now time.Time) {
	route := routeOf(l)
	who := "anon"
	client := ""
	switch {
	case l.Key != "":
		who, client = "key", "key:"+l.Key
	case l.SID != "":
		who, client = "session", "sid:"+l.SID
	}
	e.requests.WithLabelValues(route, l.Method, strconv.Itoa(l.Status/100)+"xx", who).Inc()
	e.latency.WithLabelValues(route).Observe(l.RT)
	if l.Up != "" {
		// nginx lists every server it tried; the last one answered.
		up := l.Up
		if i := strings.LastIndex(up, ", "); i >= 0 {
			up = up[i+2:]
		}
		e.upstream.WithLabelValues(up, strconv.Itoa(l.Status/100)+"xx").Inc()
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	b := e.current(now)
	b.requests++
	if client != "" {
		b.clients[client]++
	}
	isRead := l.Method == "GET" || l.Method == "HEAD"
	if isRead {
		b.reads++
		if l.Tok != "" {
			b.tokenReads++
		}
	}
	if isRead && l.Status == 200 && (route == "home" || route == "listing" || route == "detail") {
		k := cacheKey(route, l.URI)
		b.keys[k]++
		b.keysRoute[k] = route
	}
	if !isRead && l.Status < 400 && l.Seller != "" {
		b.sellers[l.Seller]++
		b.writes++
	}
	if route == "search" && isRead {
		e.observeSearch(b, l)
	}
}

func (e *Edge) observeSearch(b *bucket, l logLine) {
	b.searches++
	u, _ := url.Parse(l.URI)
	var q url.Values
	if u != nil {
		q = u.Query()
	}
	text := strings.ToLower(strings.TrimSpace(q.Get("q")))
	if q.Get("category") != "" || q.Get("min_price") != "" || q.Get("max_price") != "" {
		b.faceted++
	}
	if l.Results == "0" {
		b.zero++
	}
	if l.SID == "" {
		return
	}
	prev, ok := e.ses[l.SID]
	if ok {
		switch {
		case l.T-prev.t <= 1 && strings.HasPrefix(text, prev.q) && len(text) > len(prev.q):
			b.prefixes++
		case l.T-prev.t <= 5 && text != prev.q && !strings.HasPrefix(text, prev.q) && !strings.HasPrefix(prev.q, text) && editDistance(text, prev.q) <= 2:
			b.requeries++
		}
	}
	e.ses[l.SID] = lastSearch{q: text, t: l.T}
}

func (e *Edge) current(now time.Time) *bucket {
	start := now.Truncate(bucketSpan)
	if n := len(e.win); n > 0 && e.win[n-1].start.Equal(start) {
		return e.win[n-1]
	}
	e.win = append(e.win, newBucket(start))
	if len(e.win) > buckets {
		e.win = e.win[len(e.win)-buckets:]
	}
	return e.win[len(e.win)-1]
}

// Shape is the set of signals over the window, by name.
func (e *Edge) Shape(now time.Time) map[string]float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := now.Add(-buckets * bucketSpan)
	keys := map[string]uint32{}
	routeOfKey := map[string]string{}
	clients := map[string]uint32{}
	sellers := map[string]uint32{}
	var requests, writes, searches, prefixes, zero, requeries, faceted, reads, tokenReads uint32
	for _, b := range e.win {
		if b.start.Before(cutoff) {
			continue
		}
		for k, n := range b.keys {
			keys[k] += n
			routeOfKey[k] = b.keysRoute[k]
		}
		for c, n := range b.clients {
			clients[c] += n
		}
		for s, n := range b.sellers {
			sellers[s] += n
		}
		requests += b.requests
		writes += b.writes
		searches += b.searches
		prefixes += b.prefixes
		zero += b.zero
		requeries += b.requeries
		faceted += b.faceted
		reads += b.reads
		tokenReads += b.tokenReads
	}
	out := map[string]float64{}
	// Repeat ratio: 1 - distinct keys / reads, overall and per cacheable route.
	type agg struct{ distinct, total float64 }
	per := map[string]*agg{"all": {}, "home": {}, "listing": {}, "detail": {}, "cacheable": {}}
	for k, n := range keys {
		groups := []string{"all", routeOfKey[k]}
		// The cache move turns on listing and product pages; home is the microcache's.
		if r := routeOfKey[k]; r == "listing" || r == "detail" {
			groups = append(groups, "cacheable")
		}
		for _, r := range groups {
			if a, ok := per[r]; ok {
				a.distinct++
				a.total += float64(n)
			}
		}
	}
	for r, a := range per {
		out["repeat_ratio_"+r] = ratio(a.total-a.distinct, a.total)
	}
	var top uint32
	for _, n := range clients {
		top = max(top, n)
	}
	out["top_client_share"] = ratio(float64(top), float64(requests))
	out["seller_top1pct_share"] = topShare(sellers, writes, 0.01)
	out["distinct_writing_sellers"] = float64(len(sellers))
	out["search_prefix_share"] = ratio(float64(prefixes), float64(searches))
	out["search_zero_result_rate"] = ratio(float64(zero), float64(searches))
	out["search_requery_rate"] = ratio(float64(requeries), float64(searches))
	out["search_facet_share"] = ratio(float64(faceted), float64(searches))
	out["token_read_share"] = ratio(float64(tokenReads), float64(reads))
	out["window_requests"] = float64(requests)
	return out
}

func (e *Edge) publishLoop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for now := range t.C {
		for name, v := range e.Shape(now) {
			e.shape.WithLabelValues(name).Set(v)
		}
		// Forget search sessions idle for more than a minute.
		e.mu.Lock()
		cut := float64(now.Add(-time.Minute).UnixMilli()) / 1000
		for sid, s := range e.ses {
			if s.t < cut {
				delete(e.ses, sid)
			}
		}
		e.mu.Unlock()
	}
}

func ratio(a, b float64) float64 {
	if b <= 0 {
		return 0
	}
	return a / b
}

// topShare is the share of total held by the top fraction of keys (at least one).
func topShare(counts map[string]uint32, total uint32, fraction float64) float64 {
	if total == 0 || len(counts) == 0 {
		return 0
	}
	vals := make([]int, 0, len(counts))
	for _, n := range counts {
		vals = append(vals, int(n))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(vals)))
	k := max(1, int(math.Ceil(float64(len(vals))*fraction)))
	sum := 0
	for _, v := range vals[:k] {
		sum += v
	}
	return float64(sum) / float64(total)
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
