package narrator

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/lpmi-13/growingpains/internal/architect"
)

//go:embed page.html
var page []byte

// snapshot is what every viewer receives each tick: one computation, fanned out over SSE, so
// the page's load does not grow with its audience.
type snapshot struct {
	Clock        string           `json:"clock"`
	Started      bool             `json:"started"`
	Seed         uint64           `json:"seed"`
	Notes        []Note           `json:"notes"`
	Records      []Record         `json:"records"`
	Architecture *architect.Model `json:"architecture,omitempty"`
	ActiveMove   string           `json:"active_move,omitempty"`
	Migrating    []MapPlacement   `json:"migrating,omitempty"`
	Report       *RunReport       `json:"report,omitempty"`
}

func (n *Narrator) snapshot() []byte {
	n.mu.Lock()
	start := n.runStart
	seed := n.seed
	model := n.model
	move := n.activeMove
	migrating := append([]MapPlacement(nil), n.migrating...)
	report := n.report
	n.mu.Unlock()
	s := snapshot{Clock: "genesis", Started: !start.IsZero(), Seed: seed, Notes: n.Notes(), Records: n.Records(), Architecture: model, ActiveMove: move, Migrating: migrating, Report: report}
	if s.Started {
		s.Clock = Clock(time.Since(start).Seconds())
	}
	if len(s.Notes) > 60 {
		s.Notes = s.Notes[len(s.Notes)-60:]
	}
	data, _ := json.Marshal(s)
	return data
}

func (n *Narrator) broadcast() {
	data := n.snapshot()
	n.mu.Lock()
	defer n.mu.Unlock()
	for ch := range n.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

// Handler serves the narrator page, its live feed, and Start.
func (n *Narrator) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		ch := make(chan []byte, 4)
		n.mu.Lock()
		n.subs[ch] = true
		n.mu.Unlock()
		defer func() {
			n.mu.Lock()
			delete(n.subs, ch)
			n.mu.Unlock()
		}()
		fmt.Fprintf(w, "data: %s\n\n", n.snapshot())
		flusher.Flush()
		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case data := <-ch:
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			case <-keepalive.C:
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("POST /start", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		u := n.src.Crowd + "/start?" + url.Values{"seed": {r.URL.Query().Get("seed")}}.Encode()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		resp, err := n.http.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})
	mux.HandleFunc("GET /api/notes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n.Notes())
	})
	mux.HandleFunc("GET /api/records", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(n.Records())
	})
	mux.HandleFunc("GET /api/report", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		report := n.report
		n.mu.Unlock()
		if report == nil {
			http.Error(w, "run report is available after the run", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(report)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return mux
}
