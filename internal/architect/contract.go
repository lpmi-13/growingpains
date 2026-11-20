package architect

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration reads "30s" or "2m" from YAML.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Pressure is a kind of strain, armed when its condition holds for its sustain window.
type Pressure struct {
	Name    string   `yaml:"name"`
	Family  string   `yaml:"family"` // the traffic family that drives it, for the growth tree's colours
	Arm     string   `yaml:"arm"`
	Disarm  string   `yaml:"disarm"`
	Sustain Duration `yaml:"sustain"`
	// SustainWhen shortens or lengthens the evidence window in a known architecture state.
	// The arm condition still has to hold continuously; the first matching rule applies.
	SustainWhen []struct {
		When string   `yaml:"when"`
		For  Duration `yaml:"for"`
	} `yaml:"sustain_when"`
	// Lead lets the condition read ahead.*: each signal forecast this far ahead by a linear fit
	// over the last two minutes, so a move that is slow to become useful starts in time.
	Lead     Duration `yaml:"lead"`
	Priority int      `yaml:"priority"` // lower first, when several are armed
	Says     string   `yaml:"says"`     // narration template for the pressure note
}

// Placement puts part of a move's footprint on a node.
type Placement struct {
	Node      string  `yaml:"node"`      // a node name, or "any-compute"
	Component string  `yaml:"component"` // the component it adds, for the architecture map
	Kind      string  `yaml:"kind"`
	CPU       float64 `yaml:"cpu"`
	MemoryMiB float64 `yaml:"memory_mib"`
	Acquire   bool    `yaml:"acquire"` // the node is dormant until this move acquires it
}

// Step is one stage of a move. Its action runs once (actions are idempotent, so a restarted
// architect may run it again), then the step waits until wait_for and check hold, at least
// hold long, and at most timeout long.
type Step struct {
	Name    string         `yaml:"name"`
	Phase   string         `yaml:"phase"` // expand, migrate, or contract
	Action  string         `yaml:"action"`
	Params  map[string]any `yaml:"params"`
	WaitFor string         `yaml:"wait_for"` // a named readiness probe the actuator implements
	Check   string         `yaml:"check"`    // a condition over signals and state
	Hold    Duration       `yaml:"hold"`
	Timeout Duration       `yaml:"timeout"`
	Says    string         `yaml:"says"` // a short line for the decision record's Steps
}

// Contract is a move: when it is the right cure, what it costs, how it runs, and how it undoes.
type Contract struct {
	Name  string `yaml:"name"`
	Title string `yaml:"title"`
	Class string `yaml:"class"` // architecture, tuning, protective, or contract
	// BudgetProtective permits a reversible, pressure-relieving architecture move even when
	// the error budget is low. It still counts as architecture for the MVP path.
	BudgetProtective bool `yaml:"budget_protective"`
	// CooldownAfter sets the post-move wait when the next pressure can grow faster
	// than the global cooldown. Rollbacks always use the global cooldown.
	CooldownAfter Duration    `yaml:"cooldown_after"`
	Family        string      `yaml:"family"`
	Answers       string      `yaml:"answers"`       // the pressure
	Shape         []string    `yaml:"shape"`         // conditions that make this the right cure
	Preconditions []string    `yaml:"preconditions"` // required architecture state
	Footprint     []Placement `yaml:"footprint"`
	Relief        string      `yaml:"relief"`      // predicted relief, in the pressure's own units
	ReliefUnit    string      `yaml:"relief_unit"` // for the decision record
	NewComponents int         `yaml:"new_components"`
	Guards        []string    `yaml:"guards"` // must hold before every step; failing pauses, failing long rolls back
	Steps         []Step      `yaml:"steps"`
	Verify        struct {
		Settle Duration `yaml:"settle"`
		Checks []string `yaml:"checks"`
		Target string   `yaml:"target"` // the signal whose measured change is compared with the prediction
		Expect string   `yaml:"expect"` // the target's predicted value after the move, evaluated at the decision
		// ScaleWithTraffic compares like with like when the target grows with load, such as a
		// CPU ratio: the baseline and the prediction are scaled by how much traffic grew.
		ScaleWithTraffic bool `yaml:"scale_with_traffic"`
		// ReportOnly targets are measured and narrated but never judged against the
		// prediction: a latency does not scale with traffic, so across a run's growth the
		// comparison means nothing, and the checks decide.
		ReportOnly bool `yaml:"report_only"`
	} `yaml:"verify"`
	Rollback []Step `yaml:"rollback"`
	// Effects change the architecture model once the move completes.
	Effects map[string]any `yaml:"effects"`
	// Why and Rejected are narration templates: why this cure fits the traffic, and why this
	// move lost when another was chosen.
	Why      string `yaml:"why"`
	Rejected string `yaml:"rejected"`
}

// Catalog is every pressure and move contract.
type Catalog struct {
	Pressures []*Pressure
	Moves     []*Contract
}

// Move returns a contract by name.
func (c *Catalog) Move(name string) *Contract {
	for _, m := range c.Moves {
		if m.Name == name {
			return m
		}
	}
	return nil
}

// Load reads pressures.yaml and every other YAML file as a move contract.
func Load(fsys fs.FS) (*Catalog, error) {
	c := &Catalog{}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, err
		}
		if e.Name() == "pressures.yaml" {
			if err := yaml.Unmarshal(data, &c.Pressures); err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name(), err)
			}
			continue
		}
		var m Contract
		dec := yaml.NewDecoder(strings.NewReader(string(data)))
		dec.KnownFields(true)
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if m.Name != strings.TrimSuffix(path.Base(e.Name()), ".yaml") {
			return nil, fmt.Errorf("%s: name %q must match the file name", e.Name(), m.Name)
		}
		c.Moves = append(c.Moves, &m)
	}
	sort.Slice(c.Moves, func(i, j int) bool { return c.Moves[i].Name < c.Moves[j].Name })
	sort.SliceStable(c.Pressures, func(i, j int) bool { return c.Pressures[i].Priority < c.Pressures[j].Priority })
	return c, c.validate()
}

func (c *Catalog) validate() error {
	pressures := map[string]bool{}
	for _, p := range c.Pressures {
		pressures[p.Name] = true
		for _, src := range []string{p.Arm, p.Disarm} {
			if _, err := compile(src); err != nil {
				return fmt.Errorf("pressure %s: %w", p.Name, err)
			}
		}
		for _, rule := range p.SustainWhen {
			if _, err := compile(rule.When); err != nil {
				return fmt.Errorf("pressure %s sustain_when: %w", p.Name, err)
			}
		}
	}
	for _, m := range c.Moves {
		if !pressures[m.Answers] {
			return fmt.Errorf("move %s answers unknown pressure %q", m.Name, m.Answers)
		}
		switch m.Class {
		case "architecture", "tuning", "protective", "contract":
		default:
			return fmt.Errorf("move %s has unknown class %q", m.Name, m.Class)
		}
		if m.BudgetProtective && len(m.Rollback) == 0 {
			return fmt.Errorf("move %s is budget-protective but has no rollback", m.Name)
		}
		if m.CooldownAfter.D() < 0 {
			return fmt.Errorf("move %s has negative cooldown_after", m.Name)
		}
		srcs := append(append(append([]string{m.Relief}, m.Shape...), m.Preconditions...), m.Guards...)
		srcs = append(srcs, m.Verify.Checks...)
		for _, src := range []string{m.Verify.Target, m.Verify.Expect} {
			if src != "" {
				srcs = append(srcs, src)
			}
		}
		for _, s := range append(m.Steps, m.Rollback...) {
			if s.Check != "" {
				srcs = append(srcs, s.Check)
			}
		}
		for _, src := range srcs {
			if _, err := compile(src); err != nil {
				return fmt.Errorf("move %s: %w", m.Name, err)
			}
		}
		if len(m.Steps) == 0 {
			return fmt.Errorf("move %s has no steps", m.Name)
		}
		for _, s := range m.Steps {
			if s.Phase == "contract" && len(m.Rollback) == 0 {
				return fmt.Errorf("move %s has a contract step but no rollback for the steps before it", m.Name)
			}
		}
	}
	return nil
}
