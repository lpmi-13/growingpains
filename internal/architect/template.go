package architect

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"text/template"
)

// Funcs format numbers the same way everywhere a claim is written, so the narration test can
// check every quoted number against the signal it came from.
var Funcs = template.FuncMap{
	"pct": func(v any) string { return fmt.Sprintf("%.0f%%", 100*num(v)) },
	"ms":  func(v any) string { return fmt.Sprintf("%.0f ms", 1000*num(v)) },
	"s1":  func(v any) string { return fmt.Sprintf("%.1f s", num(v)) },
	"f2":  func(v any) string { return fmt.Sprintf("%.2f", num(v)) },
	"f1":  func(v any) string { return fmt.Sprintf("%.1f", num(v)) },
	"int": func(v any) string { return fmt.Sprintf("%.0f", num(v)) },
	"cores": func(v any) string {
		return fmt.Sprintf("%.2f cores", num(v))
	},
	"rps": func(v any) string { return fmt.Sprintf("%.0f req/s", num(v)) },
	"mul": func(a, b any) float64 { return num(a) * num(b) },
	"sub": func(a, b any) float64 { return num(a) - num(b) },
	"div": func(a, b any) float64 {
		if num(b) == 0 {
			return 0
		}
		return num(a) / num(b)
	},
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case nil:
		return math.NaN()
	}
	return math.NaN()
}

// expand fills a narration template from the signals and the run's variables. A template that
// cannot be filled falls back to its source text rather than inventing a number.
func expand(src string, env Env, r *Run) string {
	if !strings.Contains(src, "{{") {
		return src
	}
	data := map[string]any{}
	for k, v := range env {
		data[k] = v
	}
	if r != nil {
		data["run"] = r.Vars
	}
	return Expand(src, data)
}

// Expand fills a template from data.
func Expand(src string, data map[string]any) string {
	t, err := template.New("").Funcs(Funcs).Option("missingkey=error").Parse(src)
	if err != nil {
		return src
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return src
	}
	return strings.TrimSpace(b.String())
}
