// Package crowd plays a storyline against the shop as seeded shoppers, sellers, and visitors.
//
// Sessions start on a seeded, open-loop Poisson schedule that never waits for the shop. Each
// session draws from its own random stream, derived from the seed and its index, so
// concurrency never reorders random choices. The crowd never adapts to the system: it records
// failures and keeps going.
package crowd

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"sync"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/rng"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/traffic"
)

// Config sets up the crowd.
type Config struct {
	Base        string // the shop's front door, for example http://node-01
	MaxInflight int
	Sellers     int    // seller actors besides the sellers of hot products
	Storyline   string // storyline family
	StateFile   string // where a started run is recorded, so a restart resumes it
}

// Engine runs genesis traffic until Start, then the storyline.
type Engine struct {
	cfg     Config
	ix      *catalog.Index
	m       *Metrics
	client  *Client
	content *Content
	log     *slog.Logger

	mu      sync.Mutex
	current *phase
}

// phase is genesis or one run: a storyline, a seed, a start time, and its actors.
type phase struct {
	seed   uint64
	story  *storyline.Storyline
	start  time.Time // zero for genesis
	cancel context.CancelFunc
	ix     *catalog.Index
	picker *traffic.Picker
	lastCh int
}

func (p *phase) elapsed(at time.Time) time.Duration {
	if p.start.IsZero() {
		return -1
	}
	return at.Sub(p.start)
}

func (p *phase) state(at time.Time) storyline.State { return p.story.At(p.elapsed(at)) }

// New builds an engine. Call Run to start genesis traffic.
func New(cfg Config, ix *catalog.Index, m *Metrics, log *slog.Logger) *Engine {
	return &Engine{cfg: cfg, ix: ix, m: m, client: NewClient(cfg.Base, cfg.MaxInflight, m), content: NewContent(), log: log}
}

type savedRun struct {
	Seed      uint64    `json:"seed"`
	StartedAt time.Time `json:"started_at"`
	Storyline string    `json:"storyline"`
}

// Run plays genesis traffic, or resumes a run recorded in the state file, until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	if saved, ok := e.loadState(); ok {
		story := storyline.ByName(saved.Storyline, saved.Seed, e.ix)
		if time.Since(saved.StartedAt) < story.Duration()+10*time.Minute {
			e.log.Info("resuming run", "seed", saved.Seed, "elapsed", time.Since(saved.StartedAt).Round(time.Second))
			e.begin(ctx, saved.Seed, saved.StartedAt)
			<-ctx.Done()
			return
		}
	}
	e.begin(ctx, 0, time.Time{})
	<-ctx.Done()
}

// Start ends genesis and starts the storyline from the seed.
func (e *Engine) Start(ctx context.Context, seed uint64) error {
	e.mu.Lock()
	if e.current != nil && !e.current.start.IsZero() {
		e.mu.Unlock()
		return errAlreadyStarted
	}
	e.mu.Unlock()
	now := time.Now()
	if e.cfg.StateFile != "" {
		data, _ := json.Marshal(savedRun{Seed: seed, StartedAt: now, Storyline: e.cfg.Storyline})
		if err := os.WriteFile(e.cfg.StateFile, data, 0o644); err != nil {
			return err
		}
	}
	e.begin(ctx, seed, now)
	return nil
}

type startError string

func (s startError) Error() string { return string(s) }

const errAlreadyStarted = startError("the run has already started")

func (e *Engine) loadState() (savedRun, bool) {
	var s savedRun
	if e.cfg.StateFile == "" {
		return s, false
	}
	data, err := os.ReadFile(e.cfg.StateFile)
	if err != nil || json.Unmarshal(data, &s) != nil {
		return s, false
	}
	return s, true
}

func (e *Engine) begin(parent context.Context, seed uint64, start time.Time) {
	ctx, cancel := context.WithCancel(parent)
	story := storyline.ByName(e.cfg.Storyline, seed, e.ix)
	p := &phase{seed: seed, story: story, start: start, cancel: cancel, ix: e.ix, lastCh: -2,
		picker: traffic.NewPicker(e.ix, story, seed)}
	e.mu.Lock()
	if start.IsZero() && e.current != nil {
		// Genesis never replaces a run, even if Start won the race with Run.
		e.mu.Unlock()
		cancel()
		return
	}
	if e.current != nil {
		e.current.cancel()
	}
	e.current = p
	e.mu.Unlock()

	if !start.IsZero() {
		e.client.SetPhase("run")
		e.m.RunStarted.Set(float64(start.Unix()))
		e.m.StorySeed.Set(float64(seed))
		e.log.Info("run started", "seed", seed, "storyline", story.Name)
	}
	go e.observe(ctx, p)
	go e.arrivals(ctx, p, "shopper", e.shopperRate, func(idx uint64) { go e.shopper(ctx, p, idx) })
	go e.arrivals(ctx, p, "anonymous", e.anonRate, func(idx uint64) { go e.anonymous(ctx, p, idx) })
	for _, a := range e.sellerActors(p) {
		go a.run(ctx)
	}
}

// Status describes the crowd for the narrator page and tests.
type Status struct {
	Started   bool      `json:"started"`
	Seed      uint64    `json:"seed"`
	StartedAt time.Time `json:"started_at"`
	Elapsed   float64   `json:"elapsed_seconds"`
	Chapter   string    `json:"chapter"`
	Rate      float64   `json:"target_rps"`
	Storyline string    `json:"storyline"`
	Duration  float64   `json:"duration_seconds"`
}

// Status returns the current run state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	p := e.current
	e.mu.Unlock()
	if p == nil {
		return Status{}
	}
	now := time.Now()
	st := p.state(now)
	s := Status{Chapter: st.Name, Rate: st.Rate, Storyline: p.story.Name, Duration: p.story.Duration().Seconds()}
	if !p.start.IsZero() {
		s.Started, s.Seed, s.StartedAt, s.Elapsed = true, p.seed, p.start, now.Sub(p.start).Seconds()
	}
	return s
}

// Story returns the storyline being played. Only the run report may reveal it.
func (e *Engine) Story() *storyline.Storyline {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.current == nil {
		return nil
	}
	return e.current.story
}

func (e *Engine) observe(ctx context.Context, p *phase) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			st := p.state(now)
			e.m.StoryRate.Set(st.Rate)
			if st.Chapter != p.lastCh {
				e.m.StoryChapter.Reset()
				e.m.StoryChapter.WithLabelValues(itoa(st.Chapter+1), st.Name).Set(1)
				p.lastCh = st.Chapter
			}
			if !p.start.IsZero() {
				e.m.RunElapsed.Set(now.Sub(p.start).Seconds())
			}
		}
	}
}

// --- Arrival processes ---

// A shopper session's length: geometric with mean 5, capped at 12 steps.
const (
	sessionStepMean = 5.0
	sessionStepCap  = 12
)

// meanSessionSteps is the capped geometric's true mean, (1 - (1-p)^cap) / p, which turns the
// HTTP target into session starts. Using the uncapped 5 sent about 7% too little traffic.
var meanSessionSteps = (1 - math.Pow(1-1/sessionStepMean, sessionStepCap)) * sessionStepMean

func (e *Engine) shopperRate(st storyline.State) float64 {
	return st.Rate * math.Max(0, 1-st.Mix.AnonymousShare-st.Mix.SellerShare) / meanSessionSteps
}

func (e *Engine) anonRate(st storyline.State) float64 { return st.Rate * st.Mix.AnonymousShare }

// arrivals schedules events on a time-varying Poisson process. Times are computed ahead from
// the storyline alone, never from responses, so a slow shop cannot slow the crowd down.
func (e *Engine) arrivals(ctx context.Context, p *phase, name string, rate func(storyline.State) float64, fire func(uint64)) {
	r := rng.Stream(p.seed, "arrivals/"+name, 0)
	next := time.Now()
	if !p.start.IsZero() && p.start.After(next) {
		next = p.start
	}
	timer := time.NewTimer(0)
	<-timer.C
	for idx := uint64(0); ; idx++ {
		lam := math.Max(rate(p.state(next)), 0.01)
		next = next.Add(time.Duration(r.ExpFloat64() / lam * float64(time.Second)))
		if wait := time.Until(next); wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return
		}
		e.m.ScheduleLag.Observe(math.Max(0, -time.Until(next).Seconds()))
		e.m.Sessions.WithLabelValues(name, "scheduled").Inc()
		fire(idx)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func weighted(r *rand.Rand, w ...float64) int { return rng.Weighted(r, w) }
