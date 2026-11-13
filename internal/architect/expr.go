package architect

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"github.com/lpmi-13/growingpains/internal/signals"
)

// ErrUnknown means a condition read a signal or fact that was not measured. Unknown is never
// healthy: it never arms a pressure, never satisfies a shape, and never passes a guard.
var ErrUnknown = errors.New("unknown")

var programs sync.Map // source -> *vm.Program

func compile(src string) (*vm.Program, error) {
	if p, ok := programs.Load(src); ok {
		return p.(*vm.Program), nil
	}
	p, err := expr.Compile(src, expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile %q: %w", src, err)
	}
	programs.Store(src, p)
	return p, nil
}

// Env is what conditions and formulas can read: signals by their dotted names (api.cpu_ratio),
// the architecture as state.*, and the capacity ledger as ledger.*.
type Env map[string]any

// NewEnv nests dotted signal names into maps, so `api.cpu_ratio` reads naturally.
func NewEnv(v signals.Values, state map[string]any, ledger map[string]any) Env {
	env := Env{"state": state, "ledger": ledger}
	for name, value := range v {
		group, field, ok := strings.Cut(name, ".")
		if !ok {
			env[name] = value
			continue
		}
		m, _ := env[group].(map[string]any)
		if m == nil {
			m = map[string]any{}
			env[group] = m
		}
		m[field] = value
	}
	return env
}

// Bool evaluates a condition. A missing value anywhere in it gives ErrUnknown.
func Bool(src string, env Env) (bool, error) {
	if strings.TrimSpace(src) == "" {
		return true, nil
	}
	p, err := compile(src)
	if err != nil {
		return false, err
	}
	out, err := expr.Run(p, map[string]any(env))
	if err != nil {
		return false, fmt.Errorf("%w: %s (%v)", ErrUnknown, src, err)
	}
	b, ok := out.(bool)
	if !ok {
		if out == nil {
			return false, fmt.Errorf("%w: %s", ErrUnknown, src)
		}
		return false, fmt.Errorf("%s is %T, not a condition", src, out)
	}
	return b, nil
}

// Number evaluates a formula, such as a move's predicted relief.
func Number(src string, env Env) (float64, error) {
	p, err := compile(src)
	if err != nil {
		return 0, err
	}
	out, err := expr.Run(p, map[string]any(env))
	if err != nil {
		return 0, fmt.Errorf("%w: %s (%v)", ErrUnknown, src, err)
	}
	switch x := out.(type) {
	case float64:
		return x, nil
	case int:
		return float64(x), nil
	case nil:
		return 0, fmt.Errorf("%w: %s", ErrUnknown, src)
	}
	return 0, fmt.Errorf("%s is %T, not a number", src, out)
}

// AllOf evaluates every condition and returns the first that fails, with its reason.
func AllOf(conds []string, env Env) (bool, string) {
	for _, c := range conds {
		ok, err := Bool(c, env)
		if err != nil {
			return false, "unknown: " + c
		}
		if !ok {
			return false, c
		}
	}
	return true, ""
}
