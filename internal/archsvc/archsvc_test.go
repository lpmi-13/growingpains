package archsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMoveHistoryMetricAppearsAfterRegistration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "architect.json")
	a := &architect.Architect{Store: &architect.FileStore{Path: path}, Catalog: &architect.Catalog{}}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	if err := reg.Register(NewMetrics(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-2 * time.Minute)
	data, _ := json.Marshal(architect.Persisted{Model: architect.Genesis(), History: []*architect.Run{{
		Move: "autoscaled-fleet", Started: start, Ended: start.Add(100 * time.Second), Verdict: "verified",
	}}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Init(); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "gp_architect_move_duration_seconds" {
			if len(f.GetMetric()) != 1 || f.GetMetric()[0].GetGauge().GetValue() != 100 {
				t.Fatalf("wrong move duration metric: %v", f)
			}
			return
		}
	}
	t.Fatal("move history did not appear after it was first registered at genesis")
}
