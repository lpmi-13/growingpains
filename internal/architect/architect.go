// Package architect is the slow control loop. Every five seconds it measures the allowlisted
// signals, arms pressures that hold for their sustain window, diagnoses the shape of an armed
// pressure, ranks the moves that answer it, and runs the chosen move as a persisted step
// machine, then verifies the relief against its prediction.
//
// The twin runs this same code with a simulated signal source, actuator, and clock.
package architect

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lpmi-13/growingpains/internal/signals"
)

// Clock lets the twin run the architect faster than real time.
type Clock interface{ Now() time.Time }

// RealClock is the wall clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

// Actuator changes the shop. Actions must be idempotent: a restarted architect repeats the
// current step's action.
type Actuator interface {
	Do(ctx context.Context, action string, params map[string]any, run *Run, model *Model) error
	// Ready reports whether a named readiness probe passes, with a detail for the record.
	Ready(ctx context.Context, probe string, params map[string]any, run *Run, model *Model) (bool, string, error)
}

// Event is something the architect did or saw. The narrator, the annotations, and the run
// report are all built from these.
type Event struct {
	Seq      int                `json:"seq"`
	At       time.Time          `json:"at"`
	Kind     string             `json:"kind"` // pressure_pending, pressure_armed, pressure_cleared, decision, step, paused, resumed, verified, rolled_back, completed
	Pressure string             `json:"pressure,omitempty"`
	Move     string             `json:"move,omitempty"`
	Step     string             `json:"step,omitempty"`
	Text     string             `json:"text,omitempty"`
	Decision *Decision          `json:"decision,omitempty"`
	Signals  map[string]float64 `json:"signals,omitempty"`
	Data     map[string]any     `json:"data,omitempty"`
}

// Recorder receives events.
type Recorder interface{ Record(Event) }

// Run is one move's execution, persisted after every change so a restart resumes it.
type Run struct {
	Move         string         `json:"move"`
	Decision     Decision       `json:"decision"`
	Placed       []Resolved     `json:"placed"`
	Phase        string         `json:"phase"` // running, verifying, rolling_back, completed, rolled_back
	Step         int            `json:"step"`
	ActionDone   bool           `json:"action_done"`
	StepStarted  time.Time      `json:"step_started"`
	ActionAt     time.Time      `json:"action_at"`
	PausedSince  time.Time      `json:"paused_since"`
	Started      time.Time      `json:"started"`
	StepsEnded   time.Time      `json:"steps_ended"`
	Ended        time.Time      `json:"ended"`
	Relief       float64        `json:"relief"`    // the ranking's predicted relief, in the pressure's units
	Baseline     float64        `json:"baseline"`  // the verification target at the decision
	Expected     float64        `json:"expected"`  // the target's predicted value after the move
	After        float64        `json:"after"`     // the target's measured value after the move
	Measured     float64        `json:"measured"`  // baseline - after
	Predicted    float64        `json:"predicted"` // baseline - expected
	RateBefore   float64        `json:"rate_before"`
	RateAfter    float64        `json:"rate_after"`
	Verdict      string         `json:"verdict"`
	Lines        []string       `json:"lines"` // the decision record's Steps
	Vars         map[string]any `json:"vars"`  // values actuators keep between steps
	Contracted   bool           `json:"contracted"`
	RollbackFrom string         `json:"rollback_from,omitempty"`
}

// Persisted is everything the architect must not lose.
type Persisted struct {
	Model     *Model                    `json:"model"`
	Active    *Run                      `json:"active"`
	History   []*Run                    `json:"history"`
	Cooldown  time.Time                 `json:"cooldown"`
	Pressures map[string]*pressureState `json:"pressures"`
	Excluded  map[string]string         `json:"excluded"` // moves that under-delivered, and why
	Events    int                       `json:"events"`
}

// Store persists state.
type Store interface {
	Load() (*Persisted, error)
	Save(*Persisted) error
}

// FileStore keeps state in one JSON file, replaced atomically on every save.
type FileStore struct{ Path string }

func (f FileStore) Load() (*Persisted, error) {
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Persisted
	return &p, json.Unmarshal(data, &p)
}

func (f FileStore) Save(p *Persisted) error {
	data, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// MemoryStore keeps state in memory, for the twin and tests.
type MemoryStore struct{ P *Persisted }

func (m *MemoryStore) Load() (*Persisted, error) { return m.P, nil }
func (m *MemoryStore) Save(p *Persisted) error   { m.P = p; return nil }

// Config tunes the loop. The defaults are the plan's.
type Config struct {
	Cooldown      time.Duration // after a move, so effects can be attributed
	PauseRollback time.Duration // a guard failing this long rolls the move back
	Enabled       bool          // false runs the loop but never starts a move (the control run)
	Delay         time.Duration // waits this long after a decision before starting (the timing control)
}

// DefaultConfig is one change at a time with a 60 s cooldown.
var DefaultConfig = Config{Cooldown: 60 * time.Second, PauseRollback: 60 * time.Second, Enabled: true}

// Architect is the loop.
type Architect struct {
	Catalog *Catalog
	Source  signals.Source
	Act     Actuator
	Store   Store
	Clock   Clock
	Rec     Recorder
	Cfg     Config
	Log     *slog.Logger

	mu      sync.Mutex
	st      *Persisted
	last    signals.Values
	pending *Decision         // a decision waiting out Cfg.Delay
	history []reading         // the last two minutes of signals, for forecasts
	stuck   map[string]string // why each armed pressure has no eligible move, last recorded
}

type reading struct {
	at time.Time
	v  signals.Values
}

// forecastWindow is how much history a forecast fits.
const forecastWindow = 2 * time.Minute

// ahead forecasts every signal lead into the future by a least-squares line over the window.
// A signal needs a minute of history to be forecast; until then it is absent (unknown).
func (a *Architect) ahead(now time.Time, lead time.Duration) signals.Values {
	out := signals.Values{}
	if len(a.history) < 2 || now.Sub(a.history[0].at) < time.Minute {
		return out
	}
	names := map[string]bool{}
	for name := range a.history[len(a.history)-1].v {
		names[name] = true
	}
	for name := range names {
		var n, sx, sy, sxx, sxy float64
		for _, r := range a.history {
			y, ok := r.v[name]
			if !ok {
				continue
			}
			x := r.at.Sub(now).Seconds()
			n, sx, sy, sxx, sxy = n+1, sx+x, sy+y, sxx+x*x, sxy+x*y
		}
		if n < 6 || n*sxx-sx*sx == 0 {
			continue
		}
		slope := (n*sxy - sx*sy) / (n*sxx - sx*sx)
		intercept := (sy - slope*sx) / n
		out[name] = intercept + slope*lead.Seconds()
	}
	return out
}

// Init loads persisted state, or starts from genesis.
func (a *Architect) Init() error {
	p, err := a.Store.Load()
	if err != nil {
		return err
	}
	if p == nil {
		p = &Persisted{Model: Genesis()}
	}
	if p.Pressures == nil {
		p.Pressures = map[string]*pressureState{}
	}
	if p.Excluded == nil {
		p.Excluded = map[string]string{}
	}
	for _, pr := range a.Catalog.Pressures {
		if p.Pressures[pr.Name] == nil {
			p.Pressures[pr.Name] = &pressureState{}
		}
	}
	a.st = p
	if a.Log == nil {
		a.Log = slog.Default()
	}
	return nil
}

// Snapshot returns a copy of the persisted state and the last signals, for the API.
func (a *Architect) Snapshot() (Persisted, signals.Values) {
	a.mu.Lock()
	defer a.mu.Unlock()
	data, _ := json.Marshal(a.st)
	var cp Persisted
	json.Unmarshal(data, &cp)
	return cp, a.last
}

func (a *Architect) record(e Event) {
	a.st.Events++
	e.Seq = a.st.Events
	if e.At.IsZero() {
		e.At = a.Clock.Now()
	}
	if a.Rec != nil {
		a.Rec.Record(e)
	}
}

func (a *Architect) env(v signals.Values) Env {
	return NewEnv(v, a.st.Model.StateEnv(), a.st.Model.LedgerEnv())
}

// Tick runs one iteration of the loop.
func (a *Architect) Tick(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Clock.Now()
	v, err := a.Source.Read(ctx)
	if err != nil {
		a.Log.Warn("signals incomplete", "err", err)
	}
	a.last = v
	a.history = append(a.history, reading{at: now, v: v})
	for len(a.history) > 0 && now.Sub(a.history[0].at) > forecastWindow {
		a.history = a.history[1:]
	}
	env := a.env(v)

	// Measure and arm.
	for _, p := range a.Catalog.Pressures {
		st := a.st.Pressures[p.Name]
		penv := env
		if p.Lead.D() > 0 {
			penv = Env{}
			for k, x := range env {
				penv[k] = x
			}
			penv["ahead"] = map[string]any(NewEnv(a.ahead(now, p.Lead.D()), nil, nil))
		}
		switch st.update(p, penv, now) {
		case "pending":
			a.record(Event{Kind: "pressure_pending", Pressure: p.Name, Signals: v})
		case "armed":
			a.record(Event{Kind: "pressure_armed", Pressure: p.Name, Signals: v})
		case "cleared":
			a.record(Event{Kind: "pressure_cleared", Pressure: p.Name, Signals: v})
		}
	}

	// Execute and verify the active move.
	if r := a.st.Active; r != nil {
		a.step(ctx, r, v, env, now)
		return a.Store.Save(a.st)
	}
	if now.Before(a.st.Cooldown) {
		return a.Store.Save(a.st)
	}

	// Diagnose and rank.
	for _, p := range a.Catalog.Pressures {
		if !a.st.Pressures[p.Name].Armed {
			continue
		}
		d := Diagnose(a.Catalog, p, env, a.st.Model, a.st.Excluded, now)
		if d.Chosen == "" {
			// Say why nothing moved, once for each new set of reasons.
			var why []string
			for _, o := range d.Options {
				why = append(why, o.Move+": "+o.Reason)
			}
			key := strings.Join(why, "; ")
			if a.stuck == nil {
				a.stuck = map[string]string{}
			}
			if a.stuck[p.Name] != key {
				a.stuck[p.Name] = key
				d.Signals = v
				a.record(Event{Kind: "no_move", Pressure: p.Name, Decision: &d, Signals: v, Text: key})
			}
			continue
		}
		d.Signals = v
		if !a.Cfg.Enabled {
			// The control run: say what would have happened, once per choice.
			key := "would:" + d.Chosen
			if a.stuck == nil {
				a.stuck = map[string]string{}
			}
			if a.stuck[p.Name] != key {
				a.stuck[p.Name] = key
				a.record(Event{Kind: "would_move", Pressure: p.Name, Move: d.Chosen, Decision: &d, Signals: v, Text: d.Summary()})
			}
			break
		}
		if a.Cfg.Delay > 0 {
			if a.pending == nil || a.pending.Pressure != p.Name || a.pending.Chosen != d.Chosen {
				a.pending = &d
			}
			if now.Sub(a.pending.At) < a.Cfg.Delay {
				break
			}
			a.pending = nil
		}
		a.start(d, now, v, env)
		break
	}
	return a.Store.Save(a.st)
}

func (a *Architect) start(d Decision, now time.Time, v signals.Values, env Env) {
	c := a.Catalog.Move(d.Chosen)
	o, _ := d.Choice()
	r := &Run{Move: c.Name, Decision: d, Placed: o.Placed, Phase: "running", Started: now, StepStarted: now,
		Relief: o.Relief, Vars: map[string]any{}}
	if c.Verify.Target != "" {
		r.Baseline, _ = Number(c.Verify.Target, env)
		r.Expected = r.Baseline
		if c.Verify.Expect != "" {
			r.Expected, _ = Number(c.Verify.Expect, env)
		}
		r.Predicted = r.Baseline - r.Expected
	}
	r.RateBefore = v["traffic.rate"]
	a.st.Active = r
	a.record(Event{Kind: "decision", Pressure: d.Pressure, Move: c.Name, Decision: &d, Signals: v, Text: d.Summary()})
}

func (a *Architect) guardsHold(c *Contract, env Env) (bool, string) {
	return AllOf(c.Guards, env)
}

// step advances the active move. When a step finishes, the next one starts in the same tick,
// so instant steps do not wait for the loop.
func (a *Architect) step(ctx context.Context, r *Run, v signals.Values, env Env, now time.Time) {
	for range 32 {
		before := fmt.Sprintf("%s/%d", r.Phase, r.Step)
		a.stepOnce(ctx, r, v, env, now)
		if a.st.Active != r || fmt.Sprintf("%s/%d", r.Phase, r.Step) == before {
			return
		}
	}
}

func (a *Architect) stepOnce(ctx context.Context, r *Run, v signals.Values, env Env, now time.Time) {
	c := a.Catalog.Move(r.Move)
	switch r.Phase {
	case "running":
		if ok, why := a.guardsHold(c, env); !ok && !r.Contracted {
			if r.PausedSince.IsZero() {
				r.PausedSince = now
				a.record(Event{Kind: "paused", Move: r.Move, Step: c.Steps[r.Step].Name, Text: "guard failed: " + why, Signals: v})
			}
			if now.Sub(r.PausedSince) >= a.Cfg.PauseRollback {
				a.beginRollback(r, "guard failed for "+a.Cfg.PauseRollback.String()+": "+why, now, v)
			}
			return
		}
		if !r.PausedSince.IsZero() {
			r.PausedSince = time.Time{}
			a.record(Event{Kind: "resumed", Move: r.Move, Step: c.Steps[r.Step].Name, Signals: v})
		}
		s := c.Steps[r.Step]
		if done, failed := a.runStep(ctx, r, s, env, now); failed != "" {
			if r.Contracted {
				// Past the point of no return there is no undo; keep trying, and say so.
				a.Log.Error("step failing after contract", "move", r.Move, "step", s.Name, "why", failed)
				return
			}
			a.beginRollback(r, fmt.Sprintf("step %s: %s", s.Name, failed), now, v)
			return
		} else if !done {
			return
		}
		if s.Phase == "contract" {
			r.Contracted = true
		}
		if s.Says != "" {
			r.Lines = append(r.Lines, expand(s.Says, env, r))
		}
		a.record(Event{Kind: "step", Move: r.Move, Step: s.Name, Text: s.Says, Signals: v, Data: map[string]any{"phase": s.Phase}})
		r.Step++
		r.ActionDone, r.StepStarted = false, now
		if r.Step == len(c.Steps) {
			r.Phase, r.StepsEnded = "verifying", now
			a.st.Model.Apply(c, r.Placed)
		}
	case "verifying":
		if now.Sub(r.StepsEnded) < c.Verify.Settle.D() {
			return
		}
		ok, why := AllOf(c.Verify.Checks, env)
		if c.Verify.Target != "" {
			r.After, _ = Number(c.Verify.Target, env)
			r.RateAfter = v["traffic.rate"]
			if c.Verify.ScaleWithTraffic && r.RateBefore > 0 && r.RateAfter > 0 {
				// What the target would be now without the move, against what it is.
				growth := r.RateAfter / r.RateBefore
				r.Measured = r.Baseline*growth - r.After
				r.Predicted = (r.Baseline - r.Expected) * growth
			} else {
				r.Measured = r.Baseline - r.After
			}
		}
		r.Verdict = "verified"
		if !ok {
			r.Verdict = "verification failed: " + why
		} else if r.Predicted > 0 && c.Verify.Target != "" && r.Measured < r.Predicted/2 {
			r.Verdict = fmt.Sprintf("under-delivered: measured relief %.2f is under half the predicted %.2f", r.Measured, r.Predicted)
		}
		if r.Verdict != "verified" {
			a.st.Excluded[r.Move] = r.Verdict
		}
		a.finish(r, "completed", now, v)
	case "rolling_back":
		if r.Step >= len(c.Rollback) {
			a.finish(r, "rolled_back", now, v)
			return
		}
		s := c.Rollback[r.Step]
		done, failed := a.runStep(ctx, r, s, env, now)
		if failed != "" {
			a.Log.Error("rollback step failing", "move", r.Move, "step", s.Name, "why", failed)
			r.ActionDone, r.StepStarted = false, now
			return
		}
		if done {
			r.Step++
			r.ActionDone, r.StepStarted = false, now
		}
	}
}

// runStep performs a step's action once, then waits for its probe, check, and hold.
// It returns done, or a reason the step failed.
func (a *Architect) runStep(ctx context.Context, r *Run, s Step, env Env, now time.Time) (bool, string) {
	if !r.ActionDone {
		if s.Action != "" {
			if err := a.Act.Do(ctx, s.Action, s.Params, r, a.st.Model); err != nil {
				if s.Timeout.D() > 0 && now.Sub(r.StepStarted) >= s.Timeout.D() {
					return false, err.Error()
				}
				a.Log.Warn("action failed, will retry", "move", r.Move, "step", s.Name, "err", err)
				return false, ""
			}
		}
		r.ActionDone, r.ActionAt = true, now
	}
	ready := true
	detail := ""
	if s.WaitFor != "" {
		ok, d, err := a.Act.Ready(ctx, s.WaitFor, s.Params, r, a.st.Model)
		ready, detail = ok && err == nil, d
		if err != nil {
			detail = err.Error()
		}
	}
	if ready && s.Check != "" {
		ok, err := Bool(s.Check, env)
		ready = ok && err == nil
		if !ready {
			detail = "waiting for " + s.Check
		}
	}
	if ready && now.Sub(r.ActionAt) >= s.Hold.D() {
		return true, ""
	}
	if s.Timeout.D() > 0 && now.Sub(r.StepStarted) >= s.Timeout.D() {
		if detail == "" {
			detail = "timed out"
		}
		return false, "timed out after " + s.Timeout.D().String() + ": " + detail
	}
	return false, ""
}

func (a *Architect) beginRollback(r *Run, why string, now time.Time, v signals.Values) {
	r.RollbackFrom = why
	r.Phase, r.Step, r.ActionDone, r.StepStarted = "rolling_back", 0, false, now
	r.Lines = append(r.Lines, "Rolled back: "+why)
	a.record(Event{Kind: "rolling_back", Move: r.Move, Text: why, Signals: v})
}

func (a *Architect) finish(r *Run, phase string, now time.Time, v signals.Values) {
	r.Phase, r.Ended = phase, now
	kind := "completed"
	if phase == "rolled_back" {
		kind = "rolled_back"
		a.st.Excluded[r.Move] = "rolled back: " + r.RollbackFrom
	}
	a.record(Event{Kind: kind, Move: r.Move, Text: r.Verdict, Signals: v,
		Data: map[string]any{"predicted": r.Predicted, "measured": r.Measured, "baseline": r.Baseline, "expected": r.Expected, "after": r.After,
			"verdict": r.Verdict, "steps_ended": r.StepsEnded, "lines": r.Lines}})
	a.st.History = append(a.st.History, r)
	a.st.Active = nil
	a.st.Cooldown = now.Add(a.Cfg.Cooldown)
	// A completed move's pressure is re-measured from scratch.
	if p := a.st.Pressures[r.Decision.Pressure]; p != nil {
		*p = pressureState{}
	}
}

// Run ticks every interval until ctx ends.
func (a *Architect) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := a.Tick(ctx); err != nil {
			a.Log.Error("tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
