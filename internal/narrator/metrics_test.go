package narrator

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMoveErrorsAppearWhenRecordFinishes(t *testing.T) {
	n := &Narrator{}
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewMetrics(n))
	n.records = []*Record{{ID: 1, Move: "read-replica", Start: 384, Done: false}}
	before, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 0 {
		t.Fatalf("unfinished move exported: %v", before)
	}
	n.records[0].Done = true
	n.records[0].End = 471
	n.records[0].ChangeEnd = 456
	n.records[0].Verdict = "verified"
	n.records[0].Requests = 4000
	n.records[0].Errors = 3
	after, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || len(after[0].Metric) != 1 || after[0].GetName() != "gp_narrator_move_change_errors" {
		t.Fatalf("missing finished move: %v", after)
	}
	m := after[0].Metric[0]
	if m.GetGauge().GetValue() != 3 {
		t.Fatalf("wrong change-window error count: %v", m.GetGauge())
	}
	labels := map[string]string{}
	for _, label := range m.Label {
		labels[label.GetName()] = label.GetValue()
	}
	for key, want := range map[string]string{"move": "read-replica", "verdict": "verified", "requests": "4000", "decided_at": "t+06:24", "ended_at": "t+07:51", "duration_s": "87"} {
		if labels[key] != want {
			t.Errorf("label %s = %q, want %q", key, labels[key], want)
		}
	}
}
