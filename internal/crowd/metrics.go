package crowd

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics are the crowd's client-side measurements, which the judge turns into SLIs.
//
// Everything named crowd_storyline_* describes the storyline itself. The architect's signal
// allowlist must never read those, and a lint test enforces it.
type Metrics struct {
	Requests      *prometheus.CounterVec
	Duration      *prometheus.HistogramVec
	WriteAttempts *prometheus.CounterVec
	Correctness   *prometheus.CounterVec
	Sessions      *prometheus.CounterVec
	Suppressed    *prometheus.CounterVec
	ScheduleLag   prometheus.Histogram
	Inflight      prometheus.Gauge
	RunElapsed    prometheus.Gauge
	RunStarted    prometheus.Gauge

	StoryChapter *prometheus.GaugeVec
	StoryRate    prometheus.Gauge
	StorySeed    prometheus.Gauge
}

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.25, 0.3, 0.4, 0.5, 0.65, 0.8, 1, 1.5, 2, 3, 5}

// NewMetrics registers the crowd's metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_crowd_requests_total", Help: "Logical requests by journey, operation, and final outcome",
		}, []string{"phase", "journey", "op", "kind", "outcome"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "gp_crowd_request_duration_seconds", Help: "Client-side latency of answered requests", Buckets: latencyBuckets,
		}, []string{"journey", "kind"}),
		WriteAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_crowd_write_attempts_total", Help: "Every write attempt, first and retry, by outcome",
		}, []string{"op", "attempt", "outcome"}),
		Correctness: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_crowd_correctness_errors_total", Help: "Reads that contradict the ledger",
		}, []string{"kind"}),
		Sessions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_crowd_sessions_total", Help: "Sessions and direct requests scheduled, started, refused at the in-flight cap, and completed",
		}, []string{"source", "event"}),
		Suppressed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gp_crowd_steps_suppressed_total", Help: "Later journey steps not sent, and why",
		}, []string{"source", "reason"}),
		ScheduleLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "gp_crowd_schedule_lag_seconds", Help: "Actual send time minus scheduled time, for arrivals",
			Buckets: []float64{0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}),
		Inflight:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "gp_crowd_inflight_requests", Help: "Requests in flight"}),
		RunElapsed: prometheus.NewGauge(prometheus.GaugeOpts{Name: "gp_run_elapsed_seconds", Help: "Seconds since Start, or -1 before it"}),
		RunStarted: prometheus.NewGauge(prometheus.GaugeOpts{Name: "gp_run_start_time_seconds", Help: "Unix time of Start, or 0 before it"}),
		StoryChapter: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "crowd_storyline_chapter", Help: "The storyline chapter being played (1 for the current one)",
		}, []string{"index", "name"}),
		StoryRate: prometheus.NewGauge(prometheus.GaugeOpts{Name: "crowd_storyline_target_rps", Help: "The storyline's target HTTP rate"}),
		StorySeed: prometheus.NewGauge(prometheus.GaugeOpts{Name: "crowd_storyline_seed", Help: "The run's seed"}),
	}
	reg.MustRegister(m.Requests, m.Duration, m.WriteAttempts, m.Correctness, m.Sessions, m.Suppressed, m.ScheduleLag, m.Inflight)
	m.RunElapsed.Set(-1)
	for _, kind := range []string{"read_your_writes", "missing", "stale", "lost"} {
		m.Correctness.WithLabelValues(kind)
	}
	for _, j := range []struct{ journey, op, kind string }{
		{"browse", "home", "read"}, {"browse", "listing", "read"}, {"view", "detail", "read"},
		{"search", "search", "read"}, {"sell", "create", "write"}, {"sell", "patch", "write"},
		{"buy", "order", "write"},
	} {
		for _, o := range []string{"ok", "error_5xx", "timeout", "conn_error"} {
			for _, phase := range []string{"genesis", "run"} {
				m.Requests.WithLabelValues(phase, j.journey, j.op, j.kind, o)
			}
		}
	}
	return m
}

// RegisterRun adds the run clock and the storyline gauges. Only the crowd itself registers them.
func (m *Metrics) RegisterRun(reg prometheus.Registerer) {
	reg.MustRegister(m.RunElapsed, m.RunStarted, m.StoryChapter, m.StoryRate, m.StorySeed)
}
