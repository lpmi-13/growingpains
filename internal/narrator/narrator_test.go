package narrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/lpmi-13/growingpains/contracts"
	"github.com/lpmi-13/growingpains/internal/architect"
	"github.com/lpmi-13/growingpains/internal/catalog"
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
