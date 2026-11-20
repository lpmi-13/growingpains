package architect

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// pressureState tracks one pressure's sustain timer and hysteresis.
type pressureState struct {
	Since     time.Time `json:"since"` // when the arm condition began to hold; zero if it does not
	Armed     bool      `json:"armed"`
	ArmedAt   time.Time `json:"armed_at"`
	ClearFrom time.Time `json:"clear_from"` // when the disarm condition began to hold, while armed
}

// clearAfter is how long a disarm condition must hold before an armed pressure clears.
const clearAfter = 15 * time.Second

// update moves a pressure through idle → pending → armed → clearing → idle. It returns the
// transition that happened, if any: "pending", "armed", or "cleared".
func (s *pressureState) update(p *Pressure, env Env, now time.Time) string {
	arm, err := Bool(p.Arm, env)
	if err != nil {
		arm = false // unknown never arms a pressure
	}
	sustain := p.Sustain.D()
	for _, rule := range p.SustainWhen {
		if matches, err := Bool(rule.When, env); err == nil && matches {
			sustain = rule.For.D()
			break
		}
	}
	if !s.Armed {
		switch {
		case arm && s.Since.IsZero():
			s.Since = now
			if sustain == 0 {
				s.Armed, s.ArmedAt = true, now
				return "armed"
			}
			return "pending"
		case arm && now.Sub(s.Since) >= sustain:
			s.Armed, s.ArmedAt = true, now
			return "armed"
		case !arm:
			s.Since = time.Time{}
		}
		return ""
	}
	disarm := !arm
	if p.Disarm != "" {
		d, err := Bool(p.Disarm, env)
		disarm = err == nil && d
	}
	if !disarm {
		s.ClearFrom = time.Time{}
		return ""
	}
	if s.ClearFrom.IsZero() {
		s.ClearFrom = now
	}
	if now.Sub(s.ClearFrom) >= clearAfter {
		*s = pressureState{}
		return "cleared"
	}
	return ""
}

// Option is one candidate cure, eligible or not, with its predicted relief or why it lost.
type Option struct {
	Move          string     `json:"move"`
	Title         string     `json:"title"`
	Class         string     `json:"class"`
	Eligible      bool       `json:"eligible"`
	Relief        float64    `json:"relief"`
	ReliefUnit    string     `json:"relief_unit"`
	Reason        string     `json:"reason,omitempty"`
	NewComponents int        `json:"new_components"`
	Placed        []Resolved `json:"placed,omitempty"`
}

// Decision is a diagnosed pressure: every option considered and the one chosen, if any.
type Decision struct {
	At       time.Time          `json:"at"`
	Pressure string             `json:"pressure"`
	Chosen   string             `json:"chosen,omitempty"`
	Options  []Option           `json:"options"`
	Signals  map[string]float64 `json:"signals"`
}

// Choice returns the chosen option.
func (d Decision) Choice() (Option, bool) {
	for _, o := range d.Options {
		if o.Move == d.Chosen {
			return o, true
		}
	}
	return Option{}, false
}

// closeRelief is how near two reliefs must be for the simpler move to win.
const closeRelief = 0.10

// Diagnose reads the shape of an armed pressure and ranks the moves that answer it.
//
// A move is eligible when its shape conditions hold, its preconditions hold, its footprint
// fits the ledger, and, with under a quarter of the error budget left, it is protective
// or explicitly marked as a reversible, pressure-relieving architecture move.
// Eligible moves rank by predicted relief. When reliefs are within 10%, the move that adds
// fewer new components wins, and any remaining tie breaks by name.
func Diagnose(cat *Catalog, p *Pressure, env Env, model *Model, excluded map[string]string, now time.Time) Decision {
	d := Decision{At: now, Pressure: p.Name}
	budgetLow := false
	if slo, ok := env["slo"].(map[string]any); ok {
		if b, ok := slo["budget_remaining"].(float64); ok && b < 0.25 {
			budgetLow = true
		}
	}
	for _, m := range cat.Moves {
		if m.Answers != p.Name {
			continue
		}
		o := Option{Move: m.Name, Title: m.Title, Class: m.Class, ReliefUnit: m.ReliefUnit, NewComponents: m.NewComponents}
		relief, err := Number(m.Relief, env)
		if err == nil {
			o.Relief = math.Max(0, relief)
		}
		switch {
		case excluded[m.Name] != "":
			o.Reason = excluded[m.Name]
		case budgetLow && m.Class != "protective" && !m.BudgetProtective:
			o.Reason = "less than 25% of the error budget is left, so only protective moves are allowed"
		default:
			if ok, why := AllOf(m.Preconditions, env); !ok {
				o.Reason = "precondition: " + why
			} else if ok, why := AllOf(m.Shape, env); !ok {
				o.Reason = "shape: " + why
			} else if placed, why := model.Fit(m.Footprint); why != "" {
				o.Reason = why
			} else if err != nil {
				o.Reason = "relief unknown: " + err.Error()
			} else {
				o.Eligible, o.Placed = true, placed
			}
		}
		d.Options = append(d.Options, o)
	}
	eligible := []Option{}
	for _, o := range d.Options {
		if o.Eligible {
			eligible = append(eligible, o)
		}
	}
	if len(eligible) == 0 {
		return d
	}
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].Relief > eligible[j].Relief })
	top := eligible[0].Relief
	contenders := []Option{}
	for _, o := range eligible {
		if o.Relief >= top*(1-closeRelief) {
			contenders = append(contenders, o)
		}
	}
	sort.SliceStable(contenders, func(i, j int) bool {
		a, b := contenders[i], contenders[j]
		if a.NewComponents != b.NewComponents {
			return a.NewComponents < b.NewComponents
		}
		return a.Move < b.Move
	})
	d.Chosen = contenders[0].Move
	if top <= 0 {
		d.Chosen = ""
		for i := range d.Options {
			if d.Options[i].Eligible {
				d.Options[i].Reason = "predicts no relief"
			}
		}
	}
	return d
}

// Summary is a one-line account of a decision, for logs.
func (d Decision) Summary() string {
	if d.Chosen == "" {
		return fmt.Sprintf("%s: no eligible move", d.Pressure)
	}
	o, _ := d.Choice()
	return fmt.Sprintf("%s: %s (relief %.2f %s)", d.Pressure, o.Move, o.Relief, o.ReliefUnit)
}
