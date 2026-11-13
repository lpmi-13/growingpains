package crowd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Timeouts from the plan: reads give up after 2 s with no retries; writes retry once with the
// same idempotency key.
const (
	ReadTimeout  = 2 * time.Second
	WriteTimeout = 5 * time.Second
)

// ErrRefused means the in-flight cap was full, so the request was never sent.
var ErrRefused = errors.New("refused at the in-flight cap")

// Client sends the crowd's requests through the shop's public front door.
type Client struct {
	base  string
	http  *http.Client
	m     *Metrics
	slot  chan struct{}
	phase atomic.Value // "genesis" until Start, then "run"
}

// NewClient returns a client for the shop at base, capping requests in flight.
func NewClient(base string, maxInflight int, m *Metrics) *Client {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        4096,
		MaxIdleConnsPerHost: 4096,
		// nginx closes idle connections after 75 s, so close them first.
		IdleConnTimeout:    60 * time.Second,
		DisableCompression: true,
	}
	c := &Client{
		base: strings.TrimRight(base, "/"),
		http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		m:    m,
		slot: make(chan struct{}, maxInflight),
	}
	c.phase.Store("genesis")
	return c
}

// SetPhase labels every later request as genesis or run traffic.
func (c *Client) SetPhase(phase string) { c.phase.Store(phase) }

// Session is one client's identity and cookies: a browser session, a seller's tool, or nobody.
type Session struct {
	mu         sync.Mutex
	ID         string // the gp_sid cookie, empty for anonymous visitors
	APIKey     string // bots identify themselves with a key instead
	token      string
	tokenUntil time.Time
}

// Token returns the read-your-writes cookie while it is valid.
func (s *Session) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && time.Now().Before(s.tokenUntil) {
		return s.token
	}
	return ""
}

func (s *Session) absorb(resp *http.Response) {
	for _, c := range resp.Cookies() {
		if c.Name != "gp_lsn" {
			continue
		}
		s.mu.Lock()
		s.token = c.Value
		s.tokenUntil = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
		s.mu.Unlock()
	}
}

// Result is what came back from one logical request.
type Result struct {
	Status  int
	Body    []byte
	Header  http.Header
	Outcome string // ok, error_5xx, rate_limited, timeout, conn_error, refused
	Latency time.Duration
}

// Failed reports whether the request counts against availability.
func (r Result) Failed() bool {
	return r.Outcome == "error_5xx" || r.Outcome == "timeout" || r.Outcome == "conn_error"
}

func classify(status int, err error) string {
	switch {
	case err != nil:
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return "timeout"
		}
		return "conn_error"
	case status >= 500:
		return "error_5xx"
	case status == 429:
		return "rate_limited"
	default:
		return "ok"
	}
}

// Request describes one HTTP request.
type Request struct {
	Journey, Op string
	Method      string
	Path        string
	Body        []byte
	JSON        bool // ask for JSON rather than the HTML page
	Idempotency string
}

func (c *Client) attempt(ctx context.Context, s *Session, rq Request, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if rq.Body != nil {
		body = bytes.NewReader(rq.Body)
	}
	req, err := http.NewRequestWithContext(ctx, rq.Method, c.base+rq.Path, body)
	if err != nil {
		return Result{Outcome: "conn_error"}
	}
	if rq.JSON || rq.Body != nil {
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/html")
	}
	if rq.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if rq.Idempotency != "" {
		req.Header.Set("Idempotency-Key", rq.Idempotency)
	}
	req.Header.Set("User-Agent", "gp-crowd/1")
	if s != nil {
		if s.APIKey != "" {
			req.Header.Set("X-Api-Key", s.APIKey)
		}
		if s.ID != "" {
			req.AddCookie(&http.Cookie{Name: "gp_sid", Value: s.ID})
		}
		if t := s.Token(); t != "" {
			req.AddCookie(&http.Cookie{Name: "gp_lsn", Value: t})
		}
	}
	started := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{Outcome: classify(0, err), Latency: time.Since(started)}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	latency := time.Since(started)
	if err != nil {
		return Result{Status: resp.StatusCode, Outcome: classify(0, err), Latency: latency}
	}
	if s != nil {
		s.absorb(resp)
	}
	return Result{Status: resp.StatusCode, Body: data, Header: resp.Header, Outcome: classify(resp.StatusCode, nil), Latency: latency}
}

// TryAcquire takes an in-flight slot without waiting.
func (c *Client) TryAcquire() bool {
	select {
	case c.slot <- struct{}{}:
		c.m.Inflight.Inc()
		return true
	default:
		return false
	}
}

// Release returns an in-flight slot.
func (c *Client) Release() {
	<-c.slot
	c.m.Inflight.Dec()
}

// Do sends a request that already holds an in-flight slot, records it, and returns the result.
// Reads get one attempt. Writes get a second attempt with the same idempotency key if the first
// failed, and both attempts are recorded.
func (c *Client) Do(ctx context.Context, s *Session, rq Request) Result {
	kind := "read"
	timeout := ReadTimeout
	if rq.Method != http.MethodGet {
		kind, timeout = "write", WriteTimeout
	}
	res := c.attempt(ctx, s, rq, timeout)
	if kind == "write" {
		c.m.WriteAttempts.WithLabelValues(rq.Op, "first", res.Outcome).Inc()
		if res.Failed() && ctx.Err() == nil {
			res = c.attempt(ctx, s, rq, timeout)
			c.m.WriteAttempts.WithLabelValues(rq.Op, "retry", res.Outcome).Inc()
		}
	}
	c.m.Requests.WithLabelValues(c.phase.Load().(string), rq.Journey, rq.Op, kind, res.Outcome).Inc()
	if res.Status != 0 {
		c.m.Duration.WithLabelValues(rq.Journey, kind).Observe(res.Latency.Seconds())
	}
	return res
}

// Send acquires a slot, sends, and releases. A full cap returns a refused result, unsent.
func (c *Client) Send(ctx context.Context, s *Session, rq Request) Result {
	if !c.TryAcquire() {
		return Result{Outcome: "refused"}
	}
	defer c.Release()
	return c.Do(ctx, s, rq)
}

func path(format string, args ...any) string { return fmt.Sprintf(format, args...) }
