package storyline

import (
	"testing"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
)

var ix = catalog.Build(catalog.Spec{Sellers: 100, Products: 5000, Users: 1000})

func TestWordGetsOutShape(t *testing.T) {
	s := WordGetsOut(42, ix)
	if got := s.At(-time.Second); got.Chapter != -1 || got.Rate != 20 {
		t.Fatalf("genesis: %+v", got)
	}
	if len(s.Chapters) != 4 || s.Duration() != 17*time.Minute {
		t.Fatalf("chapters %d, duration %s", len(s.Chapters), s.Duration())
	}
	// Rates grow through the first three chapters and hold in the plateau.
	prev := 0.0
	for t0 := time.Duration(0); t0 < 15*time.Minute; t0 += 10 * time.Second {
		r := s.At(t0).Rate
		if r+1e-9 < prev {
			t.Fatalf("rate fell at %s: %.1f after %.1f", t0, r, prev)
		}
		prev = r
	}
	if peak := s.At(16 * time.Minute).Rate; peak < 700*0.85 || peak > 700*1.15 {
		t.Fatalf("plateau %.0f outside 700 ± 15%%", peak)
	}
	if len(s.HotProducts) != 20 {
		t.Fatalf("got %d hot products", len(s.HotProducts))
	}
	if s.At(12*time.Minute).Mix.HotShare <= 0 || s.At(4*time.Minute).Mix.HotShare != 0 {
		t.Fatal("the flash sale's hot share should appear only in its chapter")
	}
}

func TestSeedsJitterButReproduce(t *testing.T) {
	a, b, c := WordGetsOut(1, ix), WordGetsOut(1, ix), WordGetsOut(2, ix)
	if a.Chapters[0].End != b.Chapters[0].End || a.Chapters[1].RateTo != b.Chapters[1].RateTo {
		t.Fatal("one seed gave two storylines")
	}
	if a.Chapters[0].End == c.Chapters[0].End && a.Chapters[1].RateTo == c.Chapters[1].RateTo {
		t.Fatal("two seeds gave the same jitter")
	}
	for _, s := range []*Storyline{a, c} {
		if d := s.Chapters[0].End - 5*time.Minute; d < -30*time.Second || d > 30*time.Second {
			t.Fatalf("chapter boundary jitter %s exceeds ±30 s", d)
		}
	}
}

func TestCalibrateSteps(t *testing.T) {
	s := ByName("calibrate:100:300:100", 0, ix)
	if len(s.Chapters) != 3 || s.At(150*time.Second).Rate != 300 {
		t.Fatalf("calibration ramp: %d chapters, rate %.0f at 2.5 min", len(s.Chapters), s.At(150*time.Second).Rate)
	}
}
