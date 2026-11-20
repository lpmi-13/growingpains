// Package archsvc is everything around the architect's loop on ops-01: its event log, its
// Grafana annotations, its metrics, and its HTTP API for the narrator.
package archsvc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lpmi-13/growingpains/internal/architect"
)

// Log keeps every event in memory and in a JSON-lines file, and fans new events out to
// subscribers (the narrator).
type Log struct {
	mu     sync.Mutex
	events []architect.Event
	file   *os.File
	subs   map[chan architect.Event]bool
	next   []architect.Recorder
}

// OpenLog reopens the file, so a restarted architect keeps its history.
func OpenLog(path string, next ...architect.Recorder) (*Log, error) {
	l := &Log{subs: map[chan architect.Event]bool{}, next: next}
	if path == "" {
		return l, nil
	}
	if data, err := os.ReadFile(path); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var e architect.Event
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				l.events = append(l.events, e)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	l.file = f
	return l, nil
}

// Record implements architect.Recorder.
func (l *Log) Record(e architect.Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	if l.file != nil {
		data, _ := json.Marshal(e)
		l.file.Write(append(data, '\n'))
	}
	for ch := range l.subs {
		select {
		case ch <- e:
		default: // a slow subscriber misses events rather than stalling the architect
		}
	}
	l.mu.Unlock()
	for _, n := range l.next {
		n.Record(e)
	}
}

// Since returns events with a sequence number above seq.
func (l *Log) Since(seq int) []architect.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []architect.Event
	for _, e := range l.events {
		if e.Seq > seq {
			out = append(out, e)
		}
	}
	return out
}

// Subscribe returns a channel of new events and a function to stop.
func (l *Log) Subscribe() (chan architect.Event, func()) {
	ch := make(chan architect.Event, 64)
	l.mu.Lock()
	l.subs[ch] = true
	l.mu.Unlock()
	return ch, func() {
		l.mu.Lock()
		delete(l.subs, ch)
		l.mu.Unlock()
	}
}

// Grafana writes region annotations for every move, from decision to completion or rollback,
// tagged "move" and the move's name, so every graph shows when each move happened.
type Grafana struct {
	URL, User, Password string
	Log                 *slog.Logger
	mu                  sync.Mutex
	open                map[string]int64 // move -> annotation ID
	client              *http.Client
}

// NewGrafana annotates through Grafana's HTTP API.
func NewGrafana(url, user, password string, log *slog.Logger) *Grafana {
	return &Grafana{URL: url, User: user, Password: password, Log: log, open: map[string]int64{}, client: &http.Client{Timeout: 3 * time.Second}}
}

func (g *Grafana) call(method, path string, body any) (map[string]any, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(method, g.URL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(g.User, g.Password)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("grafana %s %s: %s", method, path, resp.Status)
	}
	return out, nil
}

// Record implements architect.Recorder.
func (g *Grafana) Record(e architect.Event) {
	if g == nil || g.URL == "" {
		return
	}
	go func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch e.Kind {
		case "decision":
			text := e.Move
			if e.Decision != nil {
				if o, ok := e.Decision.Choice(); ok {
					text = o.Title
				}
			}
			out, err := g.call(http.MethodPost, "/api/annotations", map[string]any{
				"time": e.At.UnixMilli(), "timeEnd": e.At.UnixMilli(), "tags": []string{"move", e.Move}, "text": text + " (in progress)",
			})
			if err != nil {
				g.Log.Warn("annotation", "err", err)
				return
			}
			if id, ok := out["id"].(float64); ok {
				g.open[e.Move] = int64(id)
			}
		case "completed", "rolled_back":
			id, ok := g.open[e.Move]
			if !ok {
				return
			}
			text := e.Move + ": " + e.Kind
			if e.Text != "" {
				text += " (" + e.Text + ")"
			}
			if _, err := g.call(http.MethodPatch, fmt.Sprintf("/api/annotations/%d", id), map[string]any{
				"timeEnd": e.At.UnixMilli(), "text": text,
			}); err != nil {
				g.Log.Warn("annotation", "err", err)
			}
			delete(g.open, e.Move)
		}
	}()
}

// Annotate writes a point annotation, for the narrator's notes.
func (g *Grafana) Annotate(at time.Time, tags []string, text string) {
	if g == nil || g.URL == "" {
		return
	}
	go func() {
		if _, err := g.call(http.MethodPost, "/api/annotations", map[string]any{"time": at.UnixMilli(), "tags": tags, "text": text}); err != nil {
			g.Log.Warn("annotation", "err", err)
		}
	}()
}

// Metrics publishes the architect's state for the dashboard: the stage, the move in progress,
// each pressure's state, each signal it reads, and each component's state for the architecture map.
type Metrics struct {
	a *architect.Architect

	stage, move, pressure, signal, component, history *prometheus.Desc
}

// NewMetrics describes the architect.
func NewMetrics(a *architect.Architect) *Metrics {
	return &Metrics{
		a:         a,
		stage:     prometheus.NewDesc("gp_architect_stage", "Architecture moves completed", nil, nil),
		move:      prometheus.NewDesc("gp_architect_move_active", "1 while a move runs", []string{"move", "phase"}, nil),
		pressure:  prometheus.NewDesc("gp_architect_pressure_armed", "1 armed, 0.5 pending (its sustain timer runs), 0 idle", []string{"pressure"}, nil),
		signal:    prometheus.NewDesc("gp_architect_signal", "Each signal as the architect last read it", []string{"signal"}, nil),
		component: prometheus.NewDesc("gp_component_state", "Each component: 1 active, 2 migrating, 3 retired", []string{"component", "kind", "node"}, nil),
		history:   prometheus.NewDesc("gp_architect_move_duration_seconds", "Finished moves, with their decision and completion times", []string{"move", "verdict", "decided_at", "ended_at", "attempt"}, nil),
	}
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{m.stage, m.move, m.pressure, m.signal, m.component, m.history} {
		ch <- d
	}
}

func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	st, sig := m.a.Snapshot()
	if st.Model == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(m.stage, prometheus.GaugeValue, float64(st.Model.Stage))
	for i, r := range st.History {
		ch <- prometheus.MustNewConstMetric(m.history, prometheus.GaugeValue, r.Ended.Sub(r.Started).Seconds(),
			r.Move, r.Verdict, r.Started.Format(time.RFC3339), r.Ended.Format(time.RFC3339), strconv.Itoa(i+1))
	}
	if st.Active != nil {
		ch <- prometheus.MustNewConstMetric(m.move, prometheus.GaugeValue, 1, st.Active.Move, st.Active.Phase)
	}
	for name, p := range st.Pressures {
		v := 0.0
		switch {
		case p.Armed:
			v = 1
		case !p.Since.IsZero():
			v = 0.5
		}
		ch <- prometheus.MustNewConstMetric(m.pressure, prometheus.GaugeValue, v, name)
	}
	for name, v := range sig {
		ch <- prometheus.MustNewConstMetric(m.signal, prometheus.GaugeValue, v, name)
	}
	migrating := map[string]bool{}
	if st.Active != nil {
		for _, p := range st.Active.Placed {
			migrating[p.Component] = true
		}
	}
	for _, c := range st.Model.Components {
		v := map[string]float64{"active": 1, "migrating": 2, "retired": 3}[c.State]
		if migrating[c.ID] && c.State == "active" {
			v = 2
		}
		ch <- prometheus.MustNewConstMetric(m.component, prometheus.GaugeValue, v, c.ID, c.Kind, c.Node)
	}
	if st.Active != nil {
		for _, p := range st.Active.Placed {
			if st.Model.Component(p.Component) == nil {
				ch <- prometheus.MustNewConstMetric(m.component, prometheus.GaugeValue, 2, p.Component, p.Kind, p.Node)
			}
		}
	}
}

// API serves the architect's state and events to the narrator.
func API(a *architect.Architect, log *Log) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		st, sig := a.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"state": st, "signals": sig})
	})
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(log.Since(after))
	})
	return mux
}
