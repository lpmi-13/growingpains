package twin

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/architect"
)

// The twin's database size, the images' units, and the scale-up move must agree: the twin
// predicts the playground only if its cap is the one the images enforce.
func TestImagesMatchPlaygroundDBCores(t *testing.T) {
	quota := regexp.MustCompile(`(?m)^CPUQuota=(\d+)%$`)
	for _, path := range []string{
		"../../rootfs/node/etc/systemd/system/postgresql@18-main.service.d/10-caps.conf",
		"../../rootfs/node/etc/systemd/system/gp-pg-replica.service",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := quota.FindSubmatch(b)
		if m == nil {
			t.Fatalf("%s: no CPUQuota", path)
		}
		pct, _ := strconv.Atoi(string(m[1]))
		if want := int(math.Round(PlaygroundDBCores * 100)); pct != want {
			t.Errorf("%s: CPUQuota=%d%%, want %d%% (PlaygroundDBCores)", path, pct, want)
		}
	}

	// The ledger books db.medium at the plan's 1.5 cores; the scale-up move raises the primary
	// by its footprint in those units, so its playground quota scales by PlaygroundDBCores / 1.5.
	cat, err := architect.Load(contracts.FS)
	if err != nil {
		t.Fatal(err)
	}
	unit := architect.Genesis().Component("pg-primary").CPU
	m := cat.Move("db-scale-up")
	raised := unit
	for _, p := range m.Footprint {
		if p.Component == "pg-primary" {
			raised += p.CPU
		}
	}
	want := fmt.Sprintf("[%.0f%%]", raised/unit*PlaygroundDBCores*100)
	if got := fmt.Sprint(m.Steps[0].Params["args"]); got != want {
		t.Errorf("db-scale-up raises the quota to %s, want %s (%.1f of %.1f ledger cores)", got, want, raised, unit)
	}
}
