package twin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"time"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/signals"
	"github.com/lpmi-13/growingpains/internal/storyline"
)

// Actuator applies the architect's actions to the simulated shop after realistic delays.
type Actuator struct {
	sys     *System
	clock   *Clock
	readyAt map[string]time.Time
	effects []effect
}

type effect struct {
	at time.Time
	do func()
}

// Delays, from Break Team's measurements and the plan's estimates. The spike replaces them.
var (
	nodeReady      = 3 * time.Second  // removing a taint
	fleetReady     = 15 * time.Second // pods from a preloaded image; spike item 4 measures it
	valkeyReady    = 10 * time.Second
	replicaReady   = 30 * time.Second
	configConverge = 3 * time.Second
)

func (a *Actuator) after(d time.Duration, do func()) {
	a.effects = append(a.effects, effect{at: a.clock.Now().Add(d), do: do})
}

// advance applies every effect that is due.
func (a *Actuator) advance(now time.Time) {
	kept := a.effects[:0]
	for _, e := range a.effects {
		if !now.Before(e.at) {
			e.do()
		} else {
			kept = append(kept, e)
		}
	}
	a.effects = kept
}

func (a *Actuator) ready(probe string, d time.Duration) {
	a.readyAt[probe] = a.clock.Now().Add(d)
}

func num(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	}
	return 0
}

// Do performs an action.
func (a *Actuator) Do(_ context.Context, action string, params map[string]any, _ *architect.Run, _ *architect.Model) error {
	s := a.sys
	switch action {
	case "nodes.acquire":
		a.ready("nodes_ready", nodeReady)
	case "kube.apply":
		switch params["manifest"] {
		case "api-fleet":
			a.ready("fleet_ready", fleetReady)
			a.ready("fleet_converged", fleetReady+configConverge)
			a.after(fleetReady, func() { s.FleetPods, s.FleetDesired = 2, 2 })
		case "valkey":
			a.ready("valkey_ready", valkeyReady)
			a.after(valkeyReady, func() { s.CacheUp = true })
		}
	case "kube.delete":
		switch params["manifest"] {
		case "api-fleet":
			s.FleetPods, s.FleetDesired = 0, 0
		case "valkey":
			s.CacheUp, s.CachePct = false, 0
		}
	case "edge.weights":
		s.LegacyWeight, s.FleetWeight = num(params["legacy"]), num(params["fleet"])
	case "edge.remove_legacy":
		s.LegacyWeight = 0
	case "node.run":
		switch params["script"] {
		case "api-legacy-stop":
			s.LegacyUp = false
		case "pg-replica-create":
			a.ready("replica_streaming", replicaReady)
			a.after(replicaReady, func() { s.ReplicaUp = true })
		case "pg-replica-remove":
			s.ReplicaUp, s.ReplicaPct = false, 0
		case "pg-cap":
			s.PrimaryCores = 2.0
		}
	case "config.set":
		a.ready("config_converged", configConverge)
		if rr, ok := params["read_routing"].(map[string]any); ok {
			if pct, ok := rr["replica_pct"]; ok {
				v := num(pct)
				a.after(configConverge, func() { s.ReplicaPct = v })
			}
			if w, ok := rr["primary_weight"]; ok {
				v := num(w)
				a.after(configConverge, func() { s.PrimaryWeight = v })
			}
		}
		if c, ok := params["cache"].(map[string]any); ok {
			if pct, ok := c["read_pct"]; ok {
				v := num(pct)
				a.after(configConverge, func() { s.CachePct = v })
			}
		}
	case "kube.rollout":
		if env, ok := params["env"].(map[string]any); ok && fmt.Sprint(env["GP_DATABASE_URL"]) == "${pooler_dsn}" {
			a.ready("fleet_ready", fleetReady)
			a.after(fleetReady, func() { s.Pooled = true })
		} else {
			a.ready("fleet_ready", fleetReady)
			a.after(fleetReady, func() { s.Pooled = false })
		}
	case "sql.primary", "nodes.release":
	default:
		return fmt.Errorf("the twin does not know action %q", action)
	}
	return nil
}

// Ready reports whether a probe has passed.
func (a *Actuator) Ready(_ context.Context, probe string, _ map[string]any, _ *architect.Run, _ *architect.Model) (bool, string, error) {
	at, ok := a.readyAt[probe]
	if !ok {
		return false, "not started", nil
	}
	return !a.clock.Now().Before(at), "", nil
}

// Clock is the twin's simulated time.
type Clock struct{ T time.Time }

func (c *Clock) Now() time.Time { return c.T }

type source struct{ v signals.Values }

func (s *source) Read(context.Context) (signals.Values, error) { return s.v, nil }

// Options configure a twin run.
type Options struct {
	Storyline string
	Costs     Costs
	Enabled   bool          // false is the control run: pressures arm, nothing moves
	Delay     time.Duration // the timing control: every move waits this long after its decision
	Genesis   time.Duration // genesis traffic before Start
}

// DefaultOptions run the architect at the plan's settings.
var DefaultOptions = Options{Storyline: "word-gets-out", Costs: Measured, Enabled: true, Genesis: time.Minute}

// MoveResult is one move in a run's path.
type MoveResult struct {
	Move      string             `json:"move"`
	Pressure  string             `json:"pressure"`
	Decided   time.Duration      `json:"decided"`
	Migrated  time.Duration      `json:"migrated"` // when its steps finished, before verification
	Ended     time.Duration      `json:"ended"`
	Rate      float64            `json:"rate"`
	Outcome   string             `json:"outcome"`
	Verdict   string             `json:"verdict"`
	Options   []architect.Option `json:"options"`
	Shape     map[string]float64 `json:"shape"`
	Predicted float64            `json:"predicted"`
	Measured  float64            `json:"measured"`
}

// Sample is the simulated shop every 30 s.
type Sample struct {
	T      time.Duration `json:"t"`
	Rate   float64       `json:"rate"`
	API    float64       `json:"api_cpu"`
	DB     float64       `json:"db_cpu"`
	Repeat float64       `json:"repeat"`
	Errors float64       `json:"error_ratio"`
	Pods   int           `json:"pods"`
	Armed  []string      `json:"armed"`
}

// Result is a whole run.
type Result struct {
	Seed      uint64       `json:"seed"`
	Storyline string       `json:"storyline"`
	Moves     []MoveResult `json:"moves"`
	Samples   []Sample     `json:"samples"`
	Requests  float64      `json:"requests"`
	Errors    float64      `json:"errors"`
	// WorstAvailability is the lowest rolling five-minute availability after Start.
	WorstAvailability float64           `json:"worst_availability"`
	Events            []architect.Event `json:"-"`
	Start             time.Time         `json:"-"`
}

// Validate applies the plan's seed rules and returns why a seed is rejected, if it is.
//
//   - Every architecture move in the MVP storyline completes within 15 minutes (three of them).
//   - No stretch of more than seven minutes passes without a move.
//   - Each decision's deciding shape is clear of its boundary: the repeat ratio at a replica
//     decision is under 0.3, and at a cache decision over 0.7, so a real run decides the same way.
func (r Result) Validate() []string {
	var why []string
	arch := 0
	last := time.Duration(0)
	for _, m := range r.Moves {
		if m.Outcome != "completed" {
			continue
		}
		if m.Decided-last > 7*time.Minute {
			why = append(why, fmt.Sprintf("no move for %s before %s", (m.Decided-last).Round(time.Second), m.Move))
		}
		last = m.Ended
		switch m.Move {
		case "autoscaled-fleet", "read-replica", "cache":
			if m.Migrated > 0 && m.Migrated <= 15*time.Minute {
				arch++
			}
		}
		rep := m.Shape["reads.repeat_ratio"]
		if m.Move == "read-replica" && rep > 0.3 {
			why = append(why, fmt.Sprintf("replica decided with repeat ratio %.2f, too near its 0.4 boundary", rep))
		}
		if m.Move == "cache" && rep < 0.7 {
			why = append(why, fmt.Sprintf("cache decided with repeat ratio %.2f, too near its 0.6 boundary", rep))
		}
	}
	if r.Storyline == "Word gets out" && arch < 3 {
		why = append(why, fmt.Sprintf("%d of the MVP's three moves completed within 15 minutes", arch))
	}
	if r.WorstAvailability < 0.995 {
		why = append(why, fmt.Sprintf("worst 5-minute availability %.4f is under 99.5%%", r.WorstAvailability))
	}
	return why
}

// Path is the sequence of completed moves, for comparing runs.
func (r Result) Path() []string {
	var out []string
	for _, m := range r.Moves {
		if m.Outcome == "completed" {
			out = append(out, m.Move)
		}
	}
	return out
}

type recorder struct{ events []architect.Event }

func (r *recorder) Record(e architect.Event) { r.events = append(r.events, e) }

// Run plays one seed's storyline against the architect and returns its path.
func Run(ix *catalog.Index, seed uint64, opts Options) (Result, error) {
	cat, err := architect.Load(contracts.FS)
	if err != nil {
		return Result{}, err
	}
	story := storyline.ByName(opts.Storyline, seed, ix)
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clock := &Clock{T: start.Add(-opts.Genesis)}
	sys := NewSystem(ix, story, seed, opts.Costs)
	act := &Actuator{sys: sys, clock: clock, readyAt: map[string]time.Time{}}
	src := &source{}
	rec := &recorder{}
	cfg := architect.DefaultConfig
	cfg.Enabled, cfg.Delay = opts.Enabled, opts.Delay
	a := &architect.Architect{Catalog: cat, Source: src, Act: act, Store: &architect.MemoryStore{}, Clock: clock, Rec: rec, Cfg: cfg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := a.Init(); err != nil {
		return Result{}, err
	}
	res := Result{Seed: seed, Storyline: story.Name, WorstAvailability: 1, Start: start}
	const step = 5 * time.Second
	end := start.Add(story.Duration())
	type window struct{ req, err float64 }
	var recent []window
	budget := 0.0
	for ; !clock.T.After(end); clock.T = clock.T.Add(step) {
		act.advance(clock.T)
		st := story.At(clock.T.Sub(start))
		v := sys.Step(st, clock.T)
		if clock.T.After(start) {
			req := st.Rate * step.Seconds()
			errs := req * v["api.error_ratio"]
			res.Requests += req
			res.Errors += errs
			// As the judge projects it: requests so far, plus the current rate for the rest of 15 minutes.
			projected := res.Requests + st.Rate*math.Max(0, (15*time.Minute-clock.T.Sub(start)).Seconds())
			budget = 1 - res.Errors/math.Max(0.005*projected, 1)
			recent = append(recent, window{req, errs})
			if len(recent) > 60 {
				recent = recent[1:]
			}
			if len(recent) == 60 {
				var r, e float64
				for _, w := range recent {
					r, e = r+w.req, e+w.err
				}
				res.WorstAvailability = math.Min(res.WorstAvailability, 1-e/r)
			}
		}
		v["slo.budget_remaining"] = math.Max(budget, -1)
		src.v = v
		if err := a.Tick(context.Background()); err != nil {
			return res, err
		}
		if t := clock.T.Sub(start); t >= 0 && t%(30*time.Second) == 0 {
			snap, _ := a.Snapshot()
			var armed []string
			for name, p := range snap.Pressures {
				if p.Armed {
					armed = append(armed, name)
				}
			}
			res.Samples = append(res.Samples, Sample{T: t, Rate: st.Rate, API: v["api.cpu_ratio"], DB: v["db.cpu_ratio"],
				Repeat: v["reads.repeat_ratio"], Errors: v["api.error_ratio"], Pods: sys.FleetPods, Armed: armed})
		}
	}
	res.Events = rec.events
	decided := map[string]architect.Event{}
	for _, e := range rec.events {
		switch e.Kind {
		case "decision":
			decided[e.Move] = e
		case "completed", "rolled_back":
			d := decided[e.Move]
			m := MoveResult{Move: e.Move, Pressure: d.Pressure, Decided: d.At.Sub(start), Ended: e.At.Sub(start),
				Rate: d.Signals["traffic.rate"], Outcome: e.Kind, Verdict: e.Text, Shape: map[string]float64{}}
			if d.Decision != nil {
				m.Options = d.Decision.Options
			}
			for _, k := range []string{"api.cpu_ratio", "db.cpu_ratio", "db.read_share", "reads.repeat_ratio", "reads.anonymous_share", "reads.token_share"} {
				m.Shape[k] = d.Signals[k]
			}
			if p, ok := e.Data["predicted"].(float64); ok {
				m.Predicted = p
			}
			if p, ok := e.Data["measured"].(float64); ok {
				m.Measured = p
			}
			if t, ok := e.Data["steps_ended"].(time.Time); ok && !t.IsZero() {
				m.Migrated = t.Sub(start)
			}
			res.Moves = append(res.Moves, m)
		}
	}
	// A move still running at the end is reported as unfinished.
	snap, _ := a.Snapshot()
	if snap.Active != nil {
		d := decided[snap.Active.Move]
		res.Moves = append(res.Moves, MoveResult{Move: snap.Active.Move, Pressure: d.Pressure, Decided: d.At.Sub(start), Outcome: "unfinished", Rate: d.Signals["traffic.rate"]})
	}
	return res, nil
}
