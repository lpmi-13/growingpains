package crowd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/traffic"
)

// fakeShop answers like the shop. With stale set, reads return the version before the latest
// edit, which a correct shop never does for the client that made it.
type fakeShop struct {
	mu       sync.Mutex
	versions map[int64]int
	next     int64
	stale    bool
	requests int
}

func (f *fakeShop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	io.Copy(io.Discard, r.Body)
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/products/"), 10, 64)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/products":
		f.next++
		pid := 1*catalog.ProductIDBase + catalog.FirstRunLocal + f.next
		f.versions[pid] = 1
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d,"description_version":1}`, pid)
	case r.Method == http.MethodPatch:
		f.versions[id]++
		if f.versions[id] < 2 {
			f.versions[id] = 2
		}
		fmt.Fprintf(w, `{"id":%d,"description_version":%d}`, id, f.versions[id])
	case r.Method == http.MethodGet && id != 0:
		v, ok := f.versions[id]
		if !ok {
			v = 1
		}
		if f.stale && v > 1 {
			v--
		}
		fmt.Fprintf(w, `{"product":{"id":%d,"description_version":%d}}`, id, v)
	case r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{}`))
	default:
		w.Write([]byte("<html></html>"))
	}
}

func newEngine(t *testing.T, base, story string) (*Engine, *Metrics) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.RegisterRun(reg)
	ix := catalog.Build(catalog.Spec{Sellers: 50, Products: 3000, Users: 1000})
	return New(Config{Base: base, MaxInflight: 500, Sellers: 5, Storyline: story}, ix, m, slog.Default()), m
}

func TestCompletedRunRemainsFinishedAfterRestart(t *testing.T) {
	shop := &fakeShop{versions: map[int64]int{}}
	srv := httptest.NewServer(shop)
	defer srv.Close()
	e, m := newEngine(t, srv.URL, "word-gets-out")
	e.cfg.StateFile = filepath.Join(t.TempDir(), "run.json")
	data, err := json.Marshal(savedRun{Seed: 19, StartedAt: time.Now().Add(-30 * time.Minute), Storyline: e.cfg.Storyline})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.cfg.StateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	deadline := time.Now().Add(time.Second)
	for !e.Status().Started && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if st := e.Status(); !st.Started || st.Seed != 19 || st.Chapter != "Finished" || st.Rate != 0 {
		t.Fatalf("resumed completed run: %+v", st)
	}
	if err := e.Start(ctx, 20); err != errAlreadyStarted {
		t.Fatalf("starting after completion: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	shop.mu.Lock()
	requests := shop.requests
	shop.mu.Unlock()
	if requests != 0 || testutil.ToFloat64(m.StoryRate) != 0 {
		t.Fatalf("completed run sent %d requests with target rate %v", requests, testutil.ToFloat64(m.StoryRate))
	}
}

func TestOpenLoopRateMatchesTheStoryline(t *testing.T) {
	shop := &fakeShop{versions: map[int64]int{}}
	srv := httptest.NewServer(shop)
	defer srv.Close()
	e, m := newEngine(t, srv.URL, "calibrate:200:200:100")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	if err := e.Start(ctx, 7); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * time.Second)
	cancel()
	// Sessions run past the window, so count scheduled HTTP rather than completed requests.
	scheduled := testutil.ToFloat64(m.Sessions.WithLabelValues("anonymous", "scheduled")) +
		meanSessionSteps*testutil.ToFloat64(m.Sessions.WithLabelValues("shopper", "scheduled")) +
		requestsPerSellerAction*testutil.ToFloat64(m.Sessions.WithLabelValues("seller", "scheduled"))
	if rate := scheduled / 4; rate < 150 || rate > 250 {
		t.Fatalf("offered %.0f req/s, want about 200", rate)
	}
}

func TestStaleReadAfterOwnWriteIsAViolation(t *testing.T) {
	shop := &fakeShop{versions: map[int64]int{}}
	srv := httptest.NewServer(shop)
	defer srv.Close()
	e, m := newEngine(t, srv.URL, "word-gets-out")
	story := storyline.WordGetsOut(1, e.ix)
	p := &phase{seed: 1, story: story, ix: e.ix, picker: traffic.NewPicker(e.ix, story, 1)}
	a := e.sellerActors(p)[0]
	ctx := context.Background()
	mix := p.story.At(0).Mix

	a.create(ctx, rng.Stream(1, "t", 0), mix)
	a.patch(ctx, rng.Stream(1, "t", 1), mix)
	if v := testutil.ToFloat64(m.Correctness.WithLabelValues("read_your_writes")); v != 0 {
		t.Fatalf("a correct shop produced %v violations", v)
	}
	shop.mu.Lock()
	shop.stale = true
	shop.mu.Unlock()
	a.patch(ctx, rng.Stream(1, "t", 2), mix)
	if v := testutil.ToFloat64(m.Correctness.WithLabelValues("read_your_writes")); v != 1 {
		t.Fatalf("a stale read after the seller's own edit gave %v violations, want 1", v)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		status int
		err    error
		want   string
	}{{200, nil, "ok"}, {404, nil, "ok"}, {409, nil, "ok"}, {429, nil, "rate_limited"}, {503, nil, "error_5xx"},
		{0, context.DeadlineExceeded, "timeout"}, {0, io.ErrUnexpectedEOF, "conn_error"}} {
		if got := classify(c.status, c.err); got != c.want {
			t.Errorf("classify(%d, %v) = %s, want %s", c.status, c.err, got, c.want)
		}
	}
}
