package narrator

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics makes each finished move's measured change-window errors visible in Grafana.
type Metrics struct {
	n      *Narrator
	errors *prometheus.Desc
}

func NewMetrics(n *Narrator) *Metrics {
	return &Metrics{n: n, errors: prometheus.NewDesc(
		"gp_narrator_move_change_errors",
		"Failed client requests during a finished architecture move's change window",
		[]string{"move", "verdict", "decided_at", "ended_at", "duration_s", "requests", "attempt"}, nil,
	)}
}

func (m *Metrics) Describe(ch chan<- *prometheus.Desc) { ch <- m.errors }

func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	for _, r := range m.n.Records() {
		if !r.Done {
			continue
		}
		ch <- prometheus.MustNewConstMetric(m.errors, prometheus.GaugeValue, r.Errors,
			r.Move, r.Verdict, Clock(r.Start), Clock(r.End),
			strconv.FormatFloat(r.End-r.Start, 'f', 0, 64),
			strconv.FormatFloat(r.Requests, 'f', 0, 64), strconv.Itoa(r.ID))
	}
}
