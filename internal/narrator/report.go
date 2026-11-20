package narrator

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/twin"
)

// RunReport reveals the seed's chapters only after the run has finished. It is built once from
// the crowd's seed, the architect's decision records, and the judge's client-side totals.
type RunReport struct {
	Seed      uint64    `json:"seed"`
	Storyline string    `json:"storyline"`
	Generated time.Time `json:"generated"`
	Text      string    `json:"text"`
}

type reportMetrics struct {
	requests, errors, worst, ryw, lost, peakAPI, peakDB *float64
	latencyOK, latencyTotal                             int
}

func (n *Narrator) promValueAt(ctx context.Context, query string, at time.Time) *float64 {
	params := url.Values{"query": {query}}
	if !at.IsZero() {
		params.Set("time", strconv.FormatInt(at.Unix(), 10))
	}
	u := n.src.Prometheus + "/api/v1/query?" + params.Encode()
	var body struct {
		Data struct {
			Result []struct {
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := n.getJSON(ctx, u, &body); err != nil || len(body.Data.Result) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(fmt.Sprint(body.Data.Result[0].Value[1]), 64)
	if err != nil {
		return nil
	}
	return &v
}

func (n *Narrator) finishReport(ctx context.Context, st Status) {
	n.mu.Lock()
	done := n.report != nil
	n.mu.Unlock()
	if done {
		return
	}
	// The crowd may drain in-flight requests for several seconds after the nominal end.
	// Query a fixed point after that drain so a narrator restart does not pick up the
	// newly restarted crowd's smaller process-local counters.
	if st.Elapsed < st.Duration+60 {
		return
	}
	settledAt := st.StartedAt.Add(time.Duration(st.Duration)*time.Second + time.Minute)
	m := reportMetrics{
		requests: n.promValueAt(ctx, "slo:run_requests:total", settledAt),
		errors:   n.promValueAt(ctx, "slo:run_errors:total", settledAt),
		worst: n.promValueAt(ctx, fmt.Sprintf("min_over_time(slo:availability:worst_rate5m[%ds])", max(int(st.Duration)-300, 5)),
			st.StartedAt.Add(time.Duration(st.Duration)*time.Second+20*time.Second)),
		ryw:     n.promValueAt(ctx, "slo:read_your_writes_violations:total", settledAt),
		lost:    n.promValueAt(ctx, "slo:acknowledged_writes_lost:total", settledAt),
		peakAPI: n.promValueAt(ctx, fmt.Sprintf("max_over_time(gp_architect_signal{signal=\"api.cpu_ratio\"}[%ds])", int(st.Duration)+60), settledAt),
		peakDB:  n.promValueAt(ctx, fmt.Sprintf("max_over_time(gp_architect_signal{signal=\"db.cpu_ratio\"}[%ds])", int(st.Duration)+60), settledAt),
	}
	for minute := 1; minute <= int(st.Duration)/60; minute++ {
		v := n.promValueAt(ctx, "slo:latency_ok:1m", st.StartedAt.Add(time.Duration(minute)*time.Minute))
		if v == nil {
			m.latencyTotal = 0
			break
		}
		m.latencyTotal++
		if *v >= 1 {
			m.latencyOK++
		}
	}
	// Allow the judge's last samples to arrive before sealing the report. A missing judge
	// eventually produces an explicitly incomplete report instead of hiding the storyline.
	ix := catalog.Build(catalog.Default)
	story := storyline.ByName(st.Storyline, st.Seed, ix)
	opts := twin.DefaultOptions
	opts.Storyline = st.Storyline
	// The playground's 40% genesis API cap is 1.075 times the local API cost model.
	opts.Costs = opts.Costs.ScaleAPI(1.075)
	prediction, err := twin.Run(ix, st.Seed, opts)
	var predicted []string
	if err == nil {
		predicted = prediction.Path()
	} else {
		n.log.Warn("report prediction", "err", err)
	}
	records := n.Records()
	for i := range records {
		r := &records[i]
		if !r.Done || r.ChangeEnd <= r.Start {
			continue
		}
		from := st.StartedAt.Add(time.Duration(r.Start * float64(time.Second)))
		to := st.StartedAt.Add(time.Duration(r.ChangeEnd * float64(time.Second)))
		errs, reqs := n.errorsDuring(ctx, from, to)
		if reqs > 0 {
			r.Errors, r.Requests = errs, reqs
			n.mu.Lock()
			if stored := n.byMove[r.Move]; stored != nil && stored.ID == r.ID {
				stored.Errors, stored.Requests = errs, reqs
			}
			n.mu.Unlock()
		}
	}
	report := &RunReport{Seed: st.Seed, Storyline: story.Name, Generated: time.Now(),
		Text: renderReport(st, story, records, n.Notes(), predicted, m)}
	n.mu.Lock()
	n.report = report
	n.mu.Unlock()
}

func reportPath(path []string) string {
	if len(path) == 0 {
		return "none"
	}
	return strings.Join(path, " → ")
}

func renderReport(st Status, story *storyline.Storyline, records []Record, notes []Note, predicted []string, metrics reportMetrics) string {
	var b strings.Builder
	fmt.Fprintf(&b, "RUN REPORT · seed %d · %s\n", st.Seed, story.Name)
	fmt.Fprintf(&b, "Started %s · duration %s\n\n", st.StartedAt.Format(time.RFC3339), Clock(st.Duration))
	var actual []string
	for _, r := range records {
		if r.Outcome == "completed" {
			actual = append(actual, r.Move)
		}
	}
	fmt.Fprintf(&b, "Predicted path  %s\n", reportPath(predicted))
	fmt.Fprintf(&b, "Observed path   %s\n", reportPath(actual))
	if predicted != nil && !slices.Equal(predicted, actual) {
		b.WriteString("The path differed; the decision records below show the signals and rejected options.\n")
	}
	if metrics.requests != nil && metrics.errors != nil {
		fmt.Fprintf(&b, "Client requests  %.0f; failed %.0f (%.3f%%)\n", *metrics.requests, *metrics.errors,
			100**metrics.errors/(*metrics.requests+1e-9))
	} else {
		b.WriteString("Client request totals unavailable from the judge.\n")
	}
	if metrics.worst != nil {
		fmt.Fprintf(&b, "Worst journey, rolling five-minute availability  %.2f%% (target 99.5%%)\n", 100**metrics.worst)
	} else {
		b.WriteString("Journey availability unavailable from the judge.\n")
	}
	if metrics.latencyTotal > 0 {
		fmt.Fprintf(&b, "Latency targets met in %d/%d one-minute windows (target at least 95%%)\n",
			metrics.latencyOK, metrics.latencyTotal)
	} else {
		b.WriteString("Latency-window compliance unavailable from the judge.\n")
	}
	if metrics.ryw != nil && metrics.lost != nil {
		fmt.Fprintf(&b, "Read-your-writes violations  %.0f; acknowledged writes lost  %.0f\n", *metrics.ryw, *metrics.lost)
	}
	if metrics.peakAPI != nil && metrics.peakDB != nil {
		fmt.Fprintf(&b, "Peak observed use of CPU limit  API %.0f%%; busiest database %.0f%%\n",
			100**metrics.peakAPI, 100**metrics.peakDB)
	}
	b.WriteString("\nMoves and their change-window errors:\n")
	for _, r := range records {
		if r.Done {
			fmt.Fprintf(&b, "  %s–%s  %s: %.0f errors in %.0f requests during change (%s)\n",
				Clock(r.Start), Clock(r.End), r.Title, r.Errors, r.Requests, r.Verdict)
		}
	}
	b.WriteString("\nThe crowd's chapters, revealed after the run:\n")
	for _, c := range story.Chapters {
		fmt.Fprintf(&b, "\n%s–%s  %s · %.0f → %.0f offered req/s\n", Clock(c.Start.Seconds()), Clock(c.End.Seconds()), c.Name, c.RateFrom, c.RateTo)
		count := 0
		for _, note := range notes {
			if note.RunT < c.Start.Seconds() || note.RunT >= c.End.Seconds() || count >= 3 {
				continue
			}
			if note.Kind == "traffic" || note.Kind == "pressure" {
				fmt.Fprintf(&b, "  %s  %s\n", Clock(note.RunT), note.Text)
				count++
			}
		}
		for _, r := range records {
			if r.Start >= c.Start.Seconds() && r.Start < c.End.Seconds() {
				fmt.Fprintf(&b, "  %s  %s: %s (%s)\n", Clock(r.Start), r.Title, r.Verdict, r.Outcome)
			}
		}
	}
	b.WriteString("\nDecision records contain each move's evidence, alternatives, steps, and measured result.\n")
	return b.String()
}
