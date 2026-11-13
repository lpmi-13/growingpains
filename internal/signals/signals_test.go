package signals

import (
	"regexp"
	"strings"
	"testing"
)

// The architect must see only the shop's telemetry and the judge's aggregated SLO burn. It must
// never read the crowd's own metrics, the storyline, or a persona label.
func TestAllowlistNeverReadsTheCrowd(t *testing.T) {
	forbidden := regexp.MustCompile(`crowd_storyline_|gp_crowd_|gp_probe_|persona|journey|crowd:`)
	seen := map[string]bool{}
	for _, d := range All {
		if seen[d.Name] {
			t.Errorf("%s is defined twice", d.Name)
		}
		seen[d.Name] = true
		if m := forbidden.FindString(d.Query); m != "" {
			t.Errorf("%s reads %q, which the architect must not see", d.Name, m)
		}
		if strings.Contains(d.Query, "slo:") && !strings.HasPrefix(d.Name, "slo.") {
			t.Errorf("%s reads the judge's rules; only slo.* signals may", d.Name)
		}
		if d.Description == "" || d.Unit == "" {
			t.Errorf("%s needs a description and a unit", d.Name)
		}
	}
}
