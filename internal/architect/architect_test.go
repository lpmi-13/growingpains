package architect

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/signals"
)

func catalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func pressure(c *Catalog, name string) *Pressure {
	for _, p := range c.Pressures {
		if p.Name == name {
			return p
		}
	}
	return nil
}

func option(d Decision, move string) Option {
	for _, o := range d.Options {
		if o.Move == move {
			return o
		}
	}
	return Option{}
}

// Signal values shaped like each MVP chapter.
var (
	chapter1 = signals.Values{"api.cpu_ratio": 0.82, "api.queue_time_p95": 0.031, "db.cpu_ratio": 0.38, "db.primary_cpu_ratio": 0.38,
		"db.read_share": 0.99, "reads.repeat_ratio": 0.30, "db.seqscan_rows": 50000, "reads.token_share": 0.03, "reads.anonymous_share": 0.2,
		"db.read_cpu_ratio": 0.37, "slo.budget_remaining": 1, "api.error_ratio": 0, "api.fleet_error_ratio": 0, "db.repl_lag": 0, "traffic.rate": 200}
	chapter2 = signals.Values{"api.cpu_ratio": 0.45, "api.queue_time_p95": 0.004, "db.cpu_ratio": 0.78, "db.primary_cpu_ratio": 0.78,
		"db.read_share": 0.96, "reads.repeat_ratio": 0.18, "db.seqscan_rows": 80000, "reads.token_share": 0.04, "reads.anonymous_share": 0.25,
		"db.read_cpu_ratio": 0.75, "slo.budget_remaining": 0.97, "api.error_ratio": 0, "db.repl_lag": 0, "traffic.rate": 450}
	chapter3 = signals.Values{"api.cpu_ratio": 0.5, "api.queue_time_p95": 0.004, "db.cpu_ratio": 0.80, "db.primary_cpu_ratio": 0.35,
		"db.read_share": 0.97, "reads.repeat_ratio": 0.74, "db.seqscan_rows": 80000, "reads.token_share": 0.04, "reads.anonymous_share": 0.22,
		"db.read_cpu_ratio": 0.56, "slo.budget_remaining": 0.95, "api.error_ratio": 0, "db.repl_lag": 0.05, "traffic.rate": 600}
)

func TestMVPDecisions(t *testing.T) {
	c := catalog(t)
	m := Genesis()
	now := time.Now()

	d := Diagnose(c, pressure(c, "api-compute"), NewEnv(chapter1, m.StateEnv(), m.LedgerEnv()), m, nil, now)
	if d.Chosen != "autoscaled-fleet" {
		t.Fatalf("chapter 1 chose %q: %+v", d.Chosen, d.Options)
	}
	if o := option(d, "api-scale-up"); o.Eligible || !strings.Contains(o.Reason, "node-01 has no spare CPU") {
		t.Fatalf("scaling up should lose on the ledger: %+v", o)
	}
	fleet, _ := d.Choice()
	m.Apply(c.Move("autoscaled-fleet"), fleet.Placed)
	if m.Facts["api_mode"] != "fleet" || m.Stage != 1 || m.Component("api-legacy") != nil {
		t.Fatalf("the fleet move did not change the model: %+v", m.Facts)
	}

	d = Diagnose(c, pressure(c, "db-read"), NewEnv(chapter2, m.StateEnv(), m.LedgerEnv()), m, nil, now)
	if d.Chosen != "read-replica" {
		t.Fatalf("chapter 2 chose %q: %+v", d.Chosen, d.Options)
	}
	if o := option(Diagnose(c, pressure(c, "read-reuse"), NewEnv(chapter2, m.StateEnv(), m.LedgerEnv()), m, nil, now), "cache"); o.Eligible || !strings.HasPrefix(o.Reason, "shape:") {
		t.Fatalf("a cache should lose on shape when reads do not repeat: %+v", o)
	}
	if o := option(d, "db-scale-up"); o.Eligible || !strings.Contains(o.Reason, "node-01 has no spare CPU") {
		t.Fatalf("vertical scale should lose on the ledger: %+v", o)
	}
	replica, _ := d.Choice()
	m.Apply(c.Move("read-replica"), replica.Placed)

	d = Diagnose(c, pressure(c, "read-reuse"), NewEnv(chapter3, m.StateEnv(), m.LedgerEnv()), m, nil, now)
	if d.Chosen != "cache" {
		t.Fatalf("chapter 3 chose %q: %+v", d.Chosen, d.Options)
	}
	if o := option(Diagnose(c, pressure(c, "db-read"), NewEnv(chapter3, m.StateEnv(), m.LedgerEnv()), m, nil, now), "second-replica"); o.Eligible || !strings.Contains(o.Reason, "no spare CPU") {
		t.Fatalf("a second replica should lose on the ledger: %+v", o)
	}
	if o := option(Diagnose(c, pressure(c, "db-read"), NewEnv(chapter3, m.StateEnv(), m.LedgerEnv()), m, nil, now), "read-replica"); o.Eligible || !strings.Contains(o.Reason, "precondition") {
		t.Fatalf("the first replica's contract should not apply again: %+v", o)
	}
}

func TestLowBudgetStillAllowsReversiblePressureRelief(t *testing.T) {
	c := catalog(t)
	m := Genesis()
	now := time.Now()
	low := func(src signals.Values) signals.Values {
		out := signals.Values{}
		for k, v := range src {
			out[k] = v
		}
		out["slo.budget_remaining"] = 0.1
		return out
	}
	for _, tc := range []struct {
		pressure string
		values   signals.Values
		want     string
	}{
		{"api-compute", chapter1, "autoscaled-fleet"},
		{"db-read", chapter2, "read-replica"},
		{"read-reuse", chapter3, "cache"},
	} {
		d := Diagnose(c, pressure(c, tc.pressure), NewEnv(low(tc.values), m.StateEnv(), m.LedgerEnv()), m, nil, now)
		if d.Chosen != tc.want {
			t.Fatalf("low-budget %s chose %q: %+v", tc.pressure, d.Chosen, d.Options)
		}
		chosen, _ := d.Choice()
		m.Apply(c.Move(tc.want), chosen.Placed)
	}
	d := Diagnose(c, pressure(c, "api-compute"), NewEnv(low(chapter1), Genesis().StateEnv(), Genesis().LedgerEnv()),
		Genesis(), map[string]string{"autoscaled-fleet": "excluded for test"}, now)
	if o := option(d, "api-scale-up"); o.Eligible || !strings.Contains(o.Reason, "only protective moves") {
		t.Fatalf("unproven tuning move should stay gated: %+v", o)
	}
}

func TestUnknownNeverArms(t *testing.T) {
	c := catalog(t)
	p := pressure(c, "api-compute")
	st := &pressureState{}
	now := time.Now()
	missing := signals.Values{"db.cpu_ratio": 0.3}
	for i := range 20 {
		if st.update(p, NewEnv(missing, nil, nil), now.Add(time.Duration(i)*5*time.Second)) != "" {
			t.Fatal("a pressure armed on unmeasured signals")
		}
	}
	// With the signal present, it arms only after its 30 s sustain window.
	var armedAt time.Duration
	for i := range 20 {
		at := time.Duration(i) * 5 * time.Second
		if st.update(p, NewEnv(chapter1, nil, nil), now.Add(at)) == "armed" {
			armedAt = at
			break
		}
	}
	if armedAt != 30*time.Second {
		t.Fatalf("armed after %s, want 30 s", armedAt)
	}
}

func TestFleetLeadingTrafficPressure(t *testing.T) {
	p := pressure(catalog(t), "api-compute")
	now := time.Now()
	st := &pressureState{}
	calm := signals.Values{"traffic.rate": 0, "api.cpu_ratio": 0.4, "api.queue_time_p95": 0}
	withForecast := func(v signals.Values) Env {
		env := NewEnv(v, nil, nil)
		env["ahead"] = map[string]any{"api": map[string]any{"cpu_ratio": 0.0}}
		return env
	}
	for i := range 20 {
		if got := st.update(p, withForecast(calm), now.Add(time.Duration(i)*5*time.Second)); got != "" {
			t.Fatalf("idle shop raised API pressure: %s", got)
		}
	}
	rising := signals.Values{"traffic.rate": 35, "api.cpu_ratio": 0.3, "api.queue_time_p95": 0}
	for i := range 6 {
		if got := st.update(p, withForecast(rising), now.Add(time.Duration(20+i)*5*time.Second)); got == "armed" {
			t.Fatal("fleet pressure armed before 30 s of rising traffic")
		}
	}
	if got := st.update(p, withForecast(rising), now.Add(130*time.Second)); got != "armed" {
		t.Fatalf("sustained rising traffic did not arm fleet pressure: %s", got)
	}
}

func TestReplicaLeadingReadTraffic(t *testing.T) {
	p := pressure(catalog(t), "db-read")
	now := time.Now()
	st := &pressureState{}
	values := signals.Values{
		"traffic.rate": 270, "db.read_share": 0.8, "db.primary_cpu_ratio": 0.4,
		"db.read_cpu_ratio": 0.4, "db.cpu_ratio": 0.4,
	}
	env := func() Env {
		e := NewEnv(values, Genesis().StateEnv(), nil)
		e["ahead"] = map[string]any{"db": map[string]any{
			"primary_cpu_ratio": 0.5, "read_cpu_ratio": 0.5,
		}}
		return e
	}
	for i := range 10 {
		if got := st.update(p, env(), now.Add(time.Duration(i)*5*time.Second)); got != "" {
			t.Fatalf("replica pressure before read-heavy rate: %s", got)
		}
	}
	values["traffic.rate"] = 300
	for i := range 9 {
		if got := st.update(p, env(), now.Add(time.Duration(10+i)*5*time.Second)); got == "armed" {
			t.Fatal("replica pressure armed before 45 s of read-heavy traffic")
		}
	}
	if got := st.update(p, env(), now.Add(95*time.Second)); got != "armed" {
		t.Fatalf("sustained read-heavy traffic did not arm replica pressure: %s", got)
	}
}

func TestReadPressureUsesShorterEvidenceWindowAfterReplica(t *testing.T) {
	p := pressure(catalog(t), "db-read")
	for _, tc := range []struct {
		replicas int
		want     time.Duration
	}{{0, 45 * time.Second}, {1, 5 * time.Second}} {
		st := &pressureState{}
		now := time.Now()
		var armedAt time.Duration
		for i := range 20 {
			at := time.Duration(i) * 5 * time.Second
			if st.update(p, NewEnv(chapter2, map[string]any{"replicas": tc.replicas}, nil), now.Add(at)) == "armed" {
				armedAt = at
				break
			}
		}
		if armedAt != tc.want {
			t.Errorf("%d replicas: armed after %s, want %s", tc.replicas, armedAt, tc.want)
		}
	}
}

func TestReadReusePressureClearsAfterCache(t *testing.T) {
	p := pressure(catalog(t), "read-reuse")
	st := &pressureState{}
	now := time.Now()
	before := NewEnv(chapter3, map[string]any{"replicas": 1, "cache": false}, nil)
	after := NewEnv(chapter3, map[string]any{"replicas": 1, "cache": true}, nil)
	for i := range 4 {
		st.update(p, before, now.Add(time.Duration(i)*5*time.Second))
	}
	if !st.Armed {
		t.Fatal("repeated reads did not arm cache pressure")
	}
	cleared := false
	for i := 4; i < 12; i++ {
		at := now.Add(time.Duration(i) * 5 * time.Second)
		if st.update(p, after, at) == "cleared" {
			cleared = true
		}
	}
	if !cleared || st.Armed {
		t.Fatal("read-reuse pressure stayed armed after the cache was installed")
	}
}

// fakeClock and fakeSource drive the loop in tests and in the twin.
type fakeClock struct{ t time.Time }

func (f *fakeClock) Now() time.Time { return f.t }

type fakeSource struct{ v signals.Values }

func (f *fakeSource) Read(context.Context) (signals.Values, error) { return f.v, nil }

type fakeActuator struct {
	actions []string
	fail    string
}

func (f *fakeActuator) Do(_ context.Context, action string, params map[string]any, _ *Run, _ *Model) error {
	f.actions = append(f.actions, action)
	return nil
}

func (f *fakeActuator) Ready(_ context.Context, probe string, _ map[string]any, _ *Run, _ *Model) (bool, string, error) {
	return probe != f.fail, "", nil
}

type events struct {
	kinds []string
	all   []Event
}

func (e *events) Record(ev Event) { e.kinds, e.all = append(e.kinds, ev.Kind), append(e.all, ev) }

// slowActuator's actions take clock time, as an SSH script does on a real node.
type slowActuator struct {
	*fakeActuator
	clock *fakeClock
	takes map[string]time.Duration
}

func (s *slowActuator) Do(ctx context.Context, action string, params map[string]any, r *Run, m *Model) error {
	s.clock.t = s.clock.t.Add(s.takes[action])
	return s.fakeActuator.Do(ctx, action, params, r, m)
}

func newArchitect(t *testing.T, v signals.Values, act *fakeActuator) (*Architect, *fakeClock, *fakeSource, *events) {
	clock := &fakeClock{t: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	src := &fakeSource{v: v}
	rec := &events{}
	a := &Architect{Catalog: catalog(t), Source: src, Act: act, Store: &MemoryStore{}, Clock: clock, Rec: rec, Cfg: DefaultConfig}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	return a, clock, src, rec
}

func tickFor(t *testing.T, a *Architect, clock *fakeClock, d time.Duration) {
	for end := clock.t.Add(d); clock.t.Before(end); clock.t = clock.t.Add(5 * time.Second) {
		if err := a.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFleetMoveRunsToCompletion(t *testing.T) {
	act := &fakeActuator{}
	a, clock, src, rec := newArchitect(t, chapter1, act)
	tickFor(t, a, clock, 40*time.Second)
	if a.st.Active == nil || a.st.Active.Move != "autoscaled-fleet" {
		t.Fatalf("no move started after the sustain window: %v", rec.kinds)
	}
	// Once traffic is on the fleet, queueing disappears.
	src.v = signals.Values{}
	for k, v := range chapter1 {
		src.v[k] = v
	}
	src.v["api.queue_time_p95"], src.v["api.cpu_ratio"] = 0.003, 0.45
	tickFor(t, a, clock, 5*time.Minute)
	if a.st.Active != nil || len(a.st.History) != 1 {
		t.Fatalf("move did not finish: active %+v", a.st.Active)
	}
	r := a.st.History[0]
	if r.Phase != "completed" || r.Verdict != "verified" {
		t.Fatalf("phase %s, verdict %q", r.Phase, r.Verdict)
	}
	want := []string{"nodes.acquire", "kube.apply", "edge.weights", "edge.weights", "edge.weights", "edge.remove_legacy", "node.run"}
	if strings.Join(act.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions %v, want %v", act.actions, want)
	}
	if a.st.Model.Stage != 1 || a.st.Model.Facts["api_mode"] != "fleet" {
		t.Fatalf("model not updated: %+v", a.st.Model.Facts)
	}
	// A shift step holds 15 s, so the migration took at least 45 s.
	if r.StepsEnded.Sub(r.Started) < 45*time.Second {
		t.Fatalf("steps took only %s", r.StepsEnded.Sub(r.Started))
	}
}

func TestReplicaCompletionUsesContractCooldown(t *testing.T) {
	a, clock, _, _ := newArchitect(t, chapter1, &fakeActuator{})
	if got := a.Catalog.Move("read-replica").CooldownAfter.D(); got != 15*time.Second {
		t.Fatalf("replica cooldown %s, want 15s", got)
	}
	a.finish(&Run{Move: "read-replica"}, "completed", clock.t, nil)
	if got := a.st.Cooldown.Sub(clock.t); got != 15*time.Second {
		t.Fatalf("completed cooldown %s, want 15s", got)
	}
	a.finish(&Run{Move: "read-replica"}, "rolled_back", clock.t, nil)
	if got := a.st.Cooldown.Sub(clock.t); got != time.Minute {
		t.Fatalf("rollback cooldown %s, want 1m", got)
	}
}

func TestFailingGuardPausesThenRollsBack(t *testing.T) {
	act := &fakeActuator{}
	a, clock, src, rec := newArchitect(t, chapter1, act)
	tickFor(t, a, clock, 50*time.Second)
	if a.st.Active == nil {
		t.Fatal("no move started")
	}
	bad := signals.Values{}
	for k, v := range chapter1 {
		bad[k] = v
	}
	bad["api.fleet_error_ratio"] = 0.2
	src.v = bad
	tickFor(t, a, clock, 3*time.Minute)
	joined := strings.Join(rec.kinds, ",")
	if !strings.Contains(joined, "paused") || !strings.Contains(joined, "rolled_back") {
		t.Fatalf("events %v", rec.kinds)
	}
	if a.st.Model.Facts["api_mode"] != "systemd" {
		t.Fatal("a rolled-back move changed the model")
	}
	if a.st.Excluded["autoscaled-fleet"] == "" {
		t.Fatal("a rolled-back move stays eligible")
	}
}

func TestDisabledArchitectNeverMoves(t *testing.T) {
	act := &fakeActuator{}
	a, clock, _, rec := newArchitect(t, chapter1, act)
	a.Cfg.Enabled = false
	tickFor(t, a, clock, 3*time.Minute)
	if a.st.Active != nil || len(act.actions) != 0 {
		t.Fatal("the control run moved")
	}
	if !strings.Contains(strings.Join(rec.kinds, ","), "pressure_armed") {
		t.Fatal("pressures should still arm in the control run")
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	act := &fakeActuator{}
	a, clock, _, _ := newArchitect(t, chapter1, act)
	a.Store = FileStore{Path: t.TempDir() + "/state.json"}
	tickFor(t, a, clock, 50*time.Second)
	b := &Architect{Catalog: a.Catalog, Source: a.Source, Act: act, Store: a.Store, Clock: clock, Cfg: DefaultConfig}
	if err := b.Init(); err != nil {
		t.Fatal(err)
	}
	if b.st.Active == nil || b.st.Active.Move != a.st.Active.Move || b.st.Active.Step != a.st.Active.Step {
		t.Fatal("a restarted architect did not resume the move")
	}
}

// A blocking action must not eat the next step's hold. On the first playground run, a 17 s base
// backup ran inside one tick, and the 20 s catch-up barrier then passed on the next tick.
func TestHoldCountsFromAfterABlockingAction(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	src := &fakeSource{v: chapter1}
	rec := &events{}
	act := &slowActuator{fakeActuator: &fakeActuator{}, clock: clock, takes: map[string]time.Duration{"node.run": 17 * time.Second}}
	a := &Architect{Catalog: catalog(t), Source: src, Act: act, Store: &MemoryStore{}, Clock: clock, Rec: rec, Cfg: DefaultConfig}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	tickFor(t, a, clock, 40*time.Second)
	calm := signals.Values{}
	for k, v := range chapter1 {
		calm[k] = v
	}
	calm["api.queue_time_p95"], calm["api.cpu_ratio"] = 0.003, 0.45
	src.v = calm
	tickFor(t, a, clock, 5*time.Minute)
	if len(a.st.History) != 1 || a.st.History[0].Verdict != "verified" {
		t.Fatalf("the fleet move did not complete: %v", rec.kinds)
	}
	src.v = chapter2
	tickFor(t, a, clock, 6*time.Minute)
	steps := map[string]time.Time{}
	for _, e := range rec.all {
		if e.Kind == "step" && e.Move == "read-replica" {
			steps[e.Step] = e.At
		}
	}
	backup, caught := steps["basebackup"], steps["catch-up"]
	if backup.IsZero() || caught.IsZero() {
		t.Fatalf("the replica move did not reach catch-up: %v", rec.kinds)
	}
	if held := caught.Sub(backup); held < 20*time.Second {
		t.Fatalf("catch-up held %s after the base backup, want at least its 20 s hold", held)
	}
}

// The fleet's queue time is reported, not judged. On a playground the pressure armed on CPU with
// queue p95 at 9.0 ms; four pods later, at 3.5 times the traffic, it was 8.2 ms. That passed the
// check, and the relief comparison wrongly called it under-delivered.
func TestFleetQueueTimeIsReportedNotJudged(t *testing.T) {
	before := signals.Values{}
	for k, v := range chapter1 {
		before[k] = v
	}
	before["api.queue_time_p95"], before["traffic.rate"] = 0.009, 97
	act := &fakeActuator{}
	a, clock, src, _ := newArchitect(t, before, act)
	tickFor(t, a, clock, 40*time.Second)
	if a.st.Active == nil || a.st.Active.Move != "autoscaled-fleet" {
		t.Fatal("no fleet move started")
	}
	after := signals.Values{}
	for k, v := range before {
		after[k] = v
	}
	after["api.queue_time_p95"], after["api.cpu_ratio"], after["traffic.rate"] = 0.0082, 0.53, 342
	src.v = after
	tickFor(t, a, clock, 5*time.Minute)
	if len(a.st.History) != 1 {
		t.Fatal("the move did not finish")
	}
	if r := a.st.History[0]; r.Verdict != "verified" {
		t.Fatalf("verdict %q (baseline %.4f, after %.4f)", r.Verdict, r.Baseline, r.After)
	}
}
