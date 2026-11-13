// Package narrator explains, in plain words, what the traffic made the system do.
//
// It writes four kinds of note from the same allowlisted signals the architect reads, so it
// never says anything the system could not have known: traffic notes when a traffic feature
// moves, pressure notes when a pressure arms, decision notes when a move starts, and outcome
// notes when a move is verified or rolled back. Notes come from deterministic templates, and
// every claim carries a number. Causes are traffic features, never guesses about people; the
// storyline's intent appears only in the run report.
package narrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lpmi-13/growingpains/internal/architect"
)

// Note is one piece of narration.
type Note struct {
	Seq     int                `json:"seq"`
	At      time.Time          `json:"at"`
	RunT    float64            `json:"run_t"` // seconds since Start, negative at genesis
	Kind    string             `json:"kind"`  // traffic, pressure, decision, outcome
	Move    string             `json:"move,omitempty"`
	Text    string             `json:"text"`
	Numbers map[string]float64 `json:"numbers"` // every number the text quotes, by signal
}

// Record is a decision record in the style of an architecture decision record.
type Record struct {
	ID      int       `json:"id"`
	Move    string    `json:"move"`
	Title   string    `json:"title"`
	Start   float64   `json:"start"`
	End     float64   `json:"end"`
	Text    string    `json:"text"`
	Done    bool      `json:"done"`
	Updated time.Time `json:"updated"`
}

// Sources are where the narrator reads from.
type Sources struct {
	Architect  string // the architect's API
	Crowd      string // the crowd's control API, for the run clock and Start
	Prometheus string // for the error count during each move's change window
}

// Annotator writes narration annotations to Grafana.
type Annotator interface {
	Annotate(at time.Time, tags []string, text string)
}

// Narrator follows the architect and writes notes.
type Narrator struct {
	src  Sources
	cat  *architect.Catalog
	ann  Annotator
	log  *slog.Logger
	http *http.Client

	mu        sync.Mutex
	notes     []Note
	records   []*Record
	byMove    map[string]*Record
	decisions map[string]architect.Event
	lastEvent int
	history   []sample // the last few minutes of signals
	lastTraf  time.Time
	runStart  time.Time
	subs      map[chan []byte]bool
}

type sample struct {
	at time.Time
	v  map[string]float64
}

// New returns a narrator for an architect's catalog.
func New(src Sources, cat *architect.Catalog, ann Annotator, log *slog.Logger) *Narrator {
	return &Narrator{src: src, cat: cat, ann: ann, log: log, http: &http.Client{Timeout: 4 * time.Second},
		byMove: map[string]*Record{}, decisions: map[string]architect.Event{}, subs: map[chan []byte]bool{}}
}

func (n *Narrator) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Run polls the architect until ctx ends.
func (n *Narrator) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Status is the crowd's run clock.
type Status struct {
	Started   bool      `json:"started"`
	StartedAt time.Time `json:"started_at"`
	Elapsed   float64   `json:"elapsed_seconds"`
	Duration  float64   `json:"duration_seconds"`
	Seed      uint64    `json:"seed"`
}

func (n *Narrator) tick(ctx context.Context) {
	var st Status
	if err := n.getJSON(ctx, n.src.Crowd+"/status", &st); err == nil && st.Started {
		n.mu.Lock()
		n.runStart = st.StartedAt
		n.mu.Unlock()
	}
	var state struct {
		Signals map[string]float64 `json:"signals"`
	}
	if err := n.getJSON(ctx, n.src.Architect+"/api/state", &state); err != nil {
		n.log.Warn("architect state", "err", err)
		return
	}
	now := time.Now()
	n.mu.Lock()
	n.history = append(n.history, sample{at: now, v: state.Signals})
	for len(n.history) > 0 && now.Sub(n.history[0].at) > 3*time.Minute {
		n.history = n.history[1:]
	}
	n.mu.Unlock()

	var events []architect.Event
	if err := n.getJSON(ctx, fmt.Sprintf("%s/api/events?after=%d", n.src.Architect, n.lastEvent), &events); err != nil {
		n.log.Warn("architect events", "err", err)
		return
	}
	for _, e := range events {
		n.lastEvent = max(n.lastEvent, e.Seq)
		n.onEvent(ctx, e)
	}
	n.trafficNote(now, state.Signals)
	n.broadcast()
}

func (n *Narrator) runT(at time.Time) float64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.runStart.IsZero() {
		return -1
	}
	return at.Sub(n.runStart).Seconds()
}

func (n *Narrator) add(note Note) {
	note.RunT = n.runT(note.At)
	n.mu.Lock()
	note.Seq = len(n.notes) + 1
	n.notes = append(n.notes, note)
	n.mu.Unlock()
	if n.ann != nil {
		n.ann.Annotate(note.At, []string{"narration", note.Kind}, strings.ToUpper(note.Kind[:1])+note.Kind[1:]+": "+note.Text)
	}
}

// Clock formats seconds since Start as t+MM:SS.
func Clock(s float64) string {
	if s < 0 {
		return "genesis"
	}
	return fmt.Sprintf("t+%02d:%02d", int(s)/60, int(s)%60)
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", 100*v) }

func (n *Narrator) pressure(name string) *architect.Pressure {
	for _, p := range n.cat.Pressures {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func envOf(v map[string]float64) map[string]any {
	return architect.NewEnv(v, nil, nil)
}

// quoted picks the signals a template mentions, so the narration test can check each number.
func quoted(tmpl string, v map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for name, x := range v {
		if strings.Contains(tmpl, "."+name) {
			out[name] = x
		}
	}
	return out
}

func (n *Narrator) onEvent(ctx context.Context, e architect.Event) {
	switch e.Kind {
	case "pressure_armed":
		p := n.pressure(e.Pressure)
		if p == nil || p.Says == "" {
			return
		}
		n.add(Note{At: e.At, Kind: "pressure", Text: architect.Expand(p.Says, envOf(e.Signals)), Numbers: quoted(p.Says, e.Signals)})
	case "decision":
		n.decide(e)
	case "completed", "rolled_back":
		n.outcome(ctx, e)
	}
}

func (n *Narrator) decide(e architect.Event) {
	c := n.cat.Move(e.Move)
	if c == nil || e.Decision == nil {
		return
	}
	env := envOf(e.Signals)
	parts := []string{c.Title + "."}
	if c.Why != "" {
		parts = append(parts, architect.Expand(c.Why, env))
	}
	for _, o := range e.Decision.Options {
		if o.Move == e.Move {
			continue
		}
		alt := n.cat.Move(o.Move)
		if alt == nil || alt.Rejected == "" {
			continue
		}
		reason := plainReason(o)
		parts = append(parts, architect.Expand(alt.Rejected, mergeEnv(env, map[string]any{"reason": reason})))
	}
	n.add(Note{At: e.At, Kind: "decision", Move: e.Move, Text: strings.Join(parts, " "), Numbers: quoted(c.Why, e.Signals)})
	start := n.runT(e.At)
	n.mu.Lock()
	n.decisions[e.Move] = e
	rec := &Record{ID: len(n.records) + 1, Move: e.Move, Title: c.Title, Start: start, Updated: time.Now()}
	n.records = append(n.records, rec)
	n.byMove[e.Move] = rec
	n.mu.Unlock()
	text := n.recordText(rec, e, nil, 0, 0)
	n.mu.Lock()
	rec.Text = text
	n.mu.Unlock()
}

func mergeEnv(a map[string]any, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// plainReason turns an option's rejection into words.
func plainReason(o architect.Option) string {
	r := o.Reason
	switch {
	case o.Eligible:
		return fmt.Sprintf("it would relieve %.2f %s, less than the chosen move", o.Relief, o.ReliefUnit)
	case strings.HasPrefix(r, "shape: "):
		return "the traffic's shape does not call for it (" + humanCondition(strings.TrimPrefix(r, "shape: ")) + " does not hold)"
	case strings.HasPrefix(r, "precondition: "):
		return "it does not apply to the system as it is (" + humanCondition(strings.TrimPrefix(r, "precondition: ")) + " does not hold)"
	}
	return r
}

func humanCondition(c string) string {
	return strings.NewReplacer("reads.repeat_ratio", "repeat ratio", "state.", "", "db.", "database ", "api.", "API ", "_", " ").Replace(c)
}

func (n *Narrator) recordText(rec *Record, d architect.Event, done *architect.Event, errs, reqs float64) string {
	var b strings.Builder
	end := "…"
	if done != nil {
		end = Clock(n.runT(done.At))
	}
	fmt.Fprintf(&b, "ADR-%03d  %-48s %s → %s\n", rec.ID, rec.Title, Clock(rec.Start), end)
	v := d.Signals
	p := n.pressure(d.Pressure)
	if p != nil && p.Says != "" {
		fmt.Fprintf(&b, "Context   %s\n", wrap(architect.Expand(p.Says, envOf(v)), 70, "          "))
	}
	if d.Decision != nil {
		opts := append([]architect.Option(nil), d.Decision.Options...)
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].Move == d.Move && opts[j].Move != d.Move })
		for i, o := range opts {
			label := "         "
			if i == 0 {
				label = "Options  "
			}
			line := fmt.Sprintf("%s: predicted relief %.2f %s", o.Title, o.Relief, shortUnit(o.ReliefUnit))
			if o.Move == d.Move {
				line += "   ← chosen"
			} else if o.Reason != "" {
				line = o.Title + ": " + plainReason(o)
			}
			fmt.Fprintf(&b, "%s %s\n", label, wrap(line, 70, "          "))
		}
	}
	if done != nil {
		var lines []string
		switch l := done.Data["lines"].(type) {
		case []string:
			lines = l
		case []any:
			for _, x := range l {
				lines = append(lines, fmt.Sprint(x))
			}
		}
		for i, l := range lines {
			label := "         "
			if i == 0 {
				label = "Steps    "
			}
			fmt.Fprintf(&b, "%s %s\n", label, wrap(l, 70, "          "))
		}
		result := n.change(n.cat.Move(rec.Move), d.Signals, done.Signals, *done)
		if done.Kind == "rolled_back" {
			result = "Rolled back: " + done.Text
		}
		if reqs > 0 {
			result += fmt.Sprintf(". Errors during change: %.0f of %.0f requests", errs, reqs)
		}
		fmt.Fprintf(&b, "Result    %s\n", wrap(result, 70, "          "))
	}
	return b.String()
}

func shortUnit(u string) string {
	if i := strings.Index(u, " of "); i > 0 && strings.HasPrefix(u, "cores") {
		return "cores"
	}
	return u
}

func wrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	var lines []string
	var cur string
	for _, w := range words {
		if cur != "" && len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return strings.Join(lines, "\n"+indent)
}

// errorsDuring asks the judge how many requests failed while a move ran.
func (n *Narrator) errorsDuring(ctx context.Context, from, to time.Time) (errs, reqs float64) {
	window := fmt.Sprintf("%ds", max(5, int(to.Sub(from).Seconds())))
	q := func(expr string) float64 {
		u := n.src.Prometheus + "/api/v1/query?" + url.Values{"query": {expr}, "time": {strconv.FormatInt(to.Unix(), 10)}}.Encode()
		var body struct {
			Data struct {
				Result []struct {
					Value [2]any `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := n.getJSON(ctx, u, &body); err != nil || len(body.Data.Result) == 0 {
			return 0
		}
		f, _ := strconv.ParseFloat(fmt.Sprint(body.Data.Result[0].Value[1]), 64)
		return f
	}
	errs = q(`sum(increase(gp_crowd_requests_total{journey!~"audit|probe|setup",outcome=~"error_5xx|timeout|conn_error"}[` + window + `]))`)
	reqs = q(`sum(increase(gp_crowd_requests_total{journey!~"audit|probe|setup"}[` + window + `]))`)
	return math.Round(errs), math.Round(reqs)
}

func (n *Narrator) outcome(ctx context.Context, e architect.Event) {
	n.mu.Lock()
	d, ok := n.decisions[e.Move]
	rec := n.byMove[e.Move]
	n.mu.Unlock()
	if !ok || rec == nil {
		return
	}
	c := n.cat.Move(e.Move)
	// The change window ends when the steps end; verification afterwards is not a change.
	windowEnd := e.At
	if t, err := time.Parse(time.RFC3339Nano, fmt.Sprint(e.Data["steps_ended"])); err == nil && t.After(d.At) {
		windowEnd = t
	}
	errs, reqs := n.errorsDuring(ctx, d.At, windowEnd)
	before, after := d.Signals, e.Signals
	var text string
	switch {
	case e.Kind == "rolled_back":
		text = fmt.Sprintf("%s was rolled back: %s.", c.Title, e.Text)
	default:
		text = n.change(c, before, after, e)
	}
	if reqs > 0 {
		text += fmt.Sprintf(" %.0f of %.0f requests failed while it ran.", errs, reqs)
	}
	nums := map[string]float64{}
	if c.Verify.Target != "" {
		target := strings.TrimSpace(c.Verify.Target)
		nums[target] = after[target]
	}
	n.add(Note{At: e.At, Kind: "outcome", Move: e.Move, Text: text, Numbers: nums})
	end := n.runT(e.At)
	text = n.recordText(rec, d, &e, errs, reqs)
	n.mu.Lock()
	rec.End, rec.Done, rec.Text, rec.Updated = end, true, text, time.Now()
	n.mu.Unlock()
}

// change says what moved, measured against the prediction.
func (n *Narrator) change(c *architect.Contract, before, after map[string]float64, e architect.Event) string {
	target := strings.TrimSpace(c.Verify.Target)
	verdict := fmt.Sprint(e.Data["verdict"])
	label := map[string]string{
		"api.queue_time_p95":   "Queue time at the API (p95)",
		"db.primary_cpu_ratio": "Primary CPU",
		"db.read_cpu_ratio":    "Database read load",
		"db.connections_ratio": "Connections in use",
	}[target]
	if label == "" || target == "" {
		return fmt.Sprintf("%s: %s.", c.Title, verdict)
	}
	format := pct
	if strings.HasSuffix(target, "_p95") {
		format = func(v float64) string { return fmt.Sprintf("%.0f ms", 1000*v) }
	}
	text := fmt.Sprintf("%s went from %s to %s", label, format(before[target]), format(after[target]))
	if exp, ok := e.Data["expected"].(float64); ok && !strings.HasSuffix(target, "_p95") {
		text += fmt.Sprintf(" (predicted %s)", format(exp))
	}
	if rb, ok := before["traffic.rate"]; ok {
		text += fmt.Sprintf(", while traffic went from %.0f to %.0f req/s", rb, after["traffic.rate"])
	}
	text += "."
	if verdict != "verified" {
		text += " " + strings.ToUpper(verdict[:1]) + verdict[1:] + "."
	}
	return text
}

// trafficThresholds are how far a feature must move in two minutes to be worth a note.
var trafficThresholds = map[string]float64{
	"traffic.rate":          0.25, // relative
	"traffic.listing_share": 0.08, "traffic.detail_share": 0.08, "traffic.write_share": 0.04,
	"reads.repeat_ratio": 0.12, "reads.anonymous_share": 0.1,
}

func (n *Narrator) trafficNote(now time.Time, v map[string]float64) {
	n.mu.Lock()
	if len(n.history) == 0 || now.Sub(n.history[0].at) < 2*time.Minute-10*time.Second {
		n.mu.Unlock()
		return
	}
	var old sample
	for _, s := range n.history {
		if now.Sub(s.at) <= 2*time.Minute {
			old = s
			break
		}
	}
	since := now.Sub(n.lastTraf)
	n.mu.Unlock()
	if since < 50*time.Second {
		return
	}
	moved := map[string]bool{}
	for k, th := range trafficThresholds {
		a, okA := old.v[k]
		b, okB := v[k]
		if !okA || !okB {
			continue
		}
		d := b - a
		if k == "traffic.rate" && a > 0 {
			d /= a
		}
		if math.Abs(d) >= th {
			moved[k] = true
		}
	}
	if len(moved) == 0 && since < 2*time.Minute {
		return
	}
	text, nums := describeTraffic(old.v, v, moved)
	n.mu.Lock()
	n.lastTraf = now
	n.mu.Unlock()
	n.add(Note{At: now, Kind: "traffic", Text: text, Numbers: nums})
}

func describeTraffic(old, cur map[string]float64, moved map[string]bool) (string, map[string]float64) {
	nums := map[string]float64{}
	var parts []string
	rate, r0 := cur["traffic.rate"], old["traffic.rate"]
	nums["traffic.rate"] = rate
	direction := "steady at"
	if moved["traffic.rate"] {
		direction = "up from"
		if rate < r0 {
			direction = "down from"
		}
		parts = append(parts, fmt.Sprintf("Traffic is %.0f req/s, %s %.0f two minutes ago.", rate, direction, r0))
	} else {
		parts = append(parts, fmt.Sprintf("Traffic is %.0f req/s.", rate))
	}
	share := func(key, what string) {
		if !moved[key] {
			return
		}
		nums[key] = cur[key]
		dir := "up"
		if cur[key] < old[key] {
			dir = "down"
		}
		parts = append(parts, fmt.Sprintf("%s are now %s of requests, %s from %s.", what, pct(cur[key]), dir, pct(old[key])))
	}
	share("traffic.listing_share", "Listing pages")
	share("traffic.detail_share", "Product pages")
	share("traffic.write_share", "Writes")
	if r, ok := cur["reads.repeat_ratio"]; ok {
		nums["reads.repeat_ratio"] = r
		switch {
		case moved["reads.repeat_ratio"] && r > old["reads.repeat_ratio"]:
			parts = append(parts, fmt.Sprintf("Views are concentrating: %s of listing and product views repeat one from the last minute, up from %s.", pct(r), pct(old["reads.repeat_ratio"])))
		case moved["reads.repeat_ratio"]:
			parts = append(parts, fmt.Sprintf("Views are spreading out: only %s of listing and product views repeat one from the last minute, down from %s.", pct(r), pct(old["reads.repeat_ratio"])))
		}
	}
	if moved["reads.anonymous_share"] {
		nums["reads.anonymous_share"] = cur["reads.anonymous_share"]
		parts = append(parts, fmt.Sprintf("%s of cacheable views come from visitors without a session.", pct(cur["reads.anonymous_share"])))
	}
	return strings.Join(parts, " "), nums
}

// Notes returns every note so far.
func (n *Narrator) Notes() []Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]Note(nil), n.notes...)
}

// Records returns every decision record so far.
func (n *Narrator) Records() []Record {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]Record, 0, len(n.records))
	for _, r := range n.records {
		out = append(out, *r)
	}
	return out
}
