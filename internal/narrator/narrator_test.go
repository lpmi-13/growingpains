package narrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/twin"
)

// Replays a twin run's real architect events through the narrator.
func narrate(t *testing.T, seed uint64) (*Narrator, twin.Result) {
	t.Helper()
	ix := catalog.Build(catalog.Default)
	res, err := twin.Run(ix, seed, twin.DefaultOptions)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := architect.Load(contracts.FS)
	n := New(Sources{}, cat, nil, slog.Default())
	n.runStart = res.Start
	for _, e := range res.Events {
		n.onEvent(context.Background(), e)
	}
	return n, res
}

func TestRunReportRevealsChaptersAndComparesPath(t *testing.T) {
	n, res := narrate(t, 1003)
	ix := catalog.Build(catalog.Default)
	story := storyline.ByName("word-gets-out", res.Seed, ix)
	st := Status{Started: true, Seed: res.Seed, StartedAt: res.Start, Storyline: story.Name,
		Elapsed: 17 * 60, Duration: story.Duration().Seconds()}
	requests, errors, worst, ryw, lost, peakAPI, peakDB := 440000.0, 0.0, 0.999, 0.0, 0.0, 0.8, 0.9
	text := renderReport(st, story, n.Records(), n.Notes(), res.Path(),
		reportMetrics{requests: &requests, errors: &errors, worst: &worst,
			ryw: &ryw, lost: &lost, peakAPI: &peakAPI, peakDB: &peakDB,
			latencyOK: 17, latencyTotal: 17})
	for _, want := range []string{"RUN REPORT", "Predicted path", "Observed path", "Client requests", "99.90%",
		"17/17 one-minute windows", "acknowledged writes lost", "Peak observed use", "Moves and their change-window errors", "The crowd's chapters"} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
	for _, chapter := range story.Chapters {
		if !strings.Contains(text, chapter.Name) {
			t.Errorf("report lacks chapter %q", chapter.Name)
		}
	}
}

func TestTrafficNoteIgnoresOneMissingRateSample(t *testing.T) {
	n := New(Sources{}, nil, nil, slog.Default())
	now := time.Now()
	old := map[string]float64{"traffic.rate": 400}
	zero := map[string]float64{"traffic.rate": 0}
	n.history = []sample{{at: now.Add(-2 * time.Minute), v: old},
		{at: now.Add(-10 * time.Second), v: old}, {at: now, v: zero}}
	n.trafficNote(now, zero)
	if len(n.Notes()) != 0 {
		t.Fatal("one zero rate sample was narrated as a traffic collapse")
	}
	n.history[1].at = now.Add(-40 * time.Second)
	n.trafficNote(now, zero)
	if len(n.Notes()) != 1 {
		t.Fatal("sustained zero traffic should still be narrated")
	}
}

func TestArchitectureSnapshotIncludesActivePlacement(t *testing.T) {
	n := New(Sources{}, nil, nil, slog.Default())
	n.model = architect.Genesis()
	n.activeMove = "read-replica"
	n.migrating = []MapPlacement{{Node: "node-04", Component: "pg-replica-1", Kind: "postgres-replica"}}
	var snap snapshot
	if err := json.Unmarshal(n.snapshot(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ActiveMove != "read-replica" || len(snap.Migrating) != 1 || snap.Migrating[0].Node != "node-04" {
		t.Fatalf("active placement missing from architecture snapshot: %+v", snap)
	}
}

func TestEveryMoveIsNarratedAndRecorded(t *testing.T) {
	n, res := narrate(t, 42)
	kinds := map[string]int{}
	for _, note := range n.Notes() {
		kinds[note.Kind]++
	}
	completed := len(res.Path())
	if kinds["decision"] != completed || kinds["outcome"] != completed || kinds["pressure"] < completed {
		t.Fatalf("notes by kind %v for %d moves", kinds, completed)
	}
	recs := n.Records()
	if len(recs) != completed {
		t.Fatalf("%d records for %d moves", len(recs), completed)
	}
	for _, r := range recs {
		for _, want := range []string{"ADR-", "Context", "Options", "← chosen", "Result"} {
			if !strings.Contains(r.Text, want) {
				t.Errorf("record for %s lacks %q:\n%s", r.Move, want, r.Text)
			}
		}
	}
	t.Logf("first record:\n%s", recs[0].Text)
}

// Every number a note quotes must appear in it as the signal's formatted value.
func TestNotesQuoteTheirSignals(t *testing.T) {
	n, _ := narrate(t, 42)
	for _, note := range n.Notes() {
		for name, v := range note.Numbers {
			var forms []string
			switch {
			case strings.HasSuffix(name, "_p95"):
				forms = []string{fmt.Sprintf("%.0f ms", 1000*v)}
			case name == "traffic.rate":
				forms = []string{fmt.Sprintf("%.0f", v)}
			default:
				forms = []string{fmt.Sprintf("%.0f%%", 100*v)}
			}
			found := false
			for _, f := range forms {
				found = found || strings.Contains(note.Text, f)
			}
			if !found {
				t.Errorf("%s note quotes %s=%v as none of %v: %q", note.Kind, name, v, forms, note.Text)
			}
		}
		if note.Kind != "outcome" && len(note.Numbers) == 0 {
			t.Errorf("%s note carries no number: %q", note.Kind, note.Text)
		}
	}
}
