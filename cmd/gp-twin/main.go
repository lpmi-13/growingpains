// Command gp-twin runs storylines through the simulated shop and the real architect.
//
//	gp-twin -seed 42              one run: a timeline and every decision
//	gp-twin -sweep 200            many seeds: paths, timing, and the seeds to reject
//	gp-twin -seed 42 -disabled    the control run: pressures arm, nothing moves
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/lpmi-13/growingpains/internal/catalog"
	"github.com/lpmi-13/growingpains/internal/storyline"
	"github.com/lpmi-13/growingpains/internal/twin"
)

func main() {
	seed := flag.Uint64("seed", 42, "seed for a single run")
	sweep := flag.Int("sweep", 0, "run seeds 1..N instead of one")
	story := flag.String("storyline", "word-gets-out", "storyline family")
	disabled := flag.Bool("disabled", false, "control run: the architect never moves")
	delay := flag.Duration("delay", 0, "timing control: every move waits this long after its decision")
	asJSON := flag.Bool("json", false, "print the result as JSON")
	explain := flag.String("explain", "", "comma-separated run times (like 3m,9m) to break the load down at, then exit")
	flag.Parse()

	ix := catalog.Build(catalog.Default)
	if *explain != "" {
		story := storyline.ByName(*story, *seed, ix)
		sys := twin.NewSystem(ix, story, *seed, twin.Measured)
		for _, at := range strings.Split(*explain, ",") {
			d, err := time.ParseDuration(at)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			st := story.At(d)
			fmt.Printf("== %s, %s, %.0f req/s\n", at, st.Name, st.Rate)
			for _, line := range sys.Explain(st) {
				fmt.Println("  " + line)
			}
		}
		return
	}
	opts := twin.DefaultOptions
	opts.Storyline, opts.Enabled, opts.Delay = *story, !*disabled, *delay
	if *sweep > 0 {
		sweepSeeds(ix, *sweep, opts)
		return
	}
	res, err := twin.Run(ix, *seed, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(res)
		return
	}
	fmt.Printf("seed %d, %s\n\n%6s %6s %5s %5s %6s %6s %4s  %s\n", res.Seed, res.Storyline, "t", "req/s", "api", "db", "repeat", "errors", "pods", "armed")
	for _, s := range res.Samples {
		fmt.Printf("%6s %6.0f %5.2f %5.2f %6.2f %6.3f %4d  %s\n", clock(s.T), s.Rate, s.API, s.DB, s.Repeat, s.Errors, s.Pods, strings.Join(s.Armed, ","))
	}
	fmt.Println()
	for _, m := range res.Moves {
		fmt.Printf("%s → %s  %-18s for %-15s at %3.0f req/s  %s %s\n", clock(m.Decided), clock(m.Ended), m.Move, m.Pressure, m.Rate, m.Outcome, m.Verdict)
		for _, o := range m.Options {
			mark := " "
			if o.Move == m.Move {
				mark = "←"
			}
			why := o.Reason
			if o.Eligible {
				why = "eligible"
			}
			fmt.Printf("      %s %-18s relief %5.2f  %s\n", mark, o.Move, o.Relief, why)
		}
		fmt.Printf("      shape %v\n", fmtShape(m.Shape))
	}
	for _, e := range res.Events {
		if e.Kind == "no_move" {
			fmt.Printf("%s  no move for %s: %s\n", clock(e.At.Sub(res.Start)), e.Pressure, e.Text)
		}
	}
	fmt.Printf("\nrequests %.0f, failed %.0f (%.3f%%), worst 5-minute availability %.4f\n", res.Requests, res.Errors, 100*res.Errors/res.Requests, res.WorstAvailability)
	if why := res.Validate(); len(why) > 0 {
		fmt.Printf("seed rejected: %s\n", strings.Join(why, "; "))
	} else {
		fmt.Println("seed valid")
	}
}

func fmtShape(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%.2f", k, m[k]))
	}
	return strings.Join(parts, " ")
}

func clock(d time.Duration) string {
	if d < 0 {
		return "-" + clock(-d)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func sweepSeeds(ix *catalog.Index, n int, opts twin.Options) {
	paths := map[string]int{}
	timing := map[string][]time.Duration{}
	var worst []float64
	reasons := map[string]int{}
	valid := 0
	for seed := uint64(1); seed <= uint64(n); seed++ {
		res, err := twin.Run(ix, seed, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, seed, err)
			continue
		}
		key := strings.Join(res.Path(), " → ")
		if key == "" {
			key = "(no moves)"
		}
		paths[key]++
		for _, m := range res.Moves {
			if m.Outcome == "completed" {
				timing[m.Move] = append(timing[m.Move], m.Decided)
			}
		}
		worst = append(worst, res.WorstAvailability)
		why := res.Validate()
		if len(why) == 0 {
			valid++
		}
		for _, w := range why {
			// Group reasons by their wording, without the numbers.
			key := strings.Map(func(r rune) rune {
				if r >= '0' && r <= '9' || r == '.' {
					return -1
				}
				return r
			}, w)
			reasons[key]++
		}
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range paths {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	fmt.Printf("%d seeds, %s\n\npaths:\n", n, opts.Storyline)
	for _, e := range list {
		fmt.Printf("  %4d  %s\n", e.v, e.k)
	}
	fmt.Println("\ndecision times (min, median, max):")
	for move, ts := range timing {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		fmt.Printf("  %-18s %s  %s  %s  (n=%d)\n", move, clock(ts[0]), clock(ts[len(ts)/2]), clock(ts[len(ts)-1]), len(ts))
	}
	sort.Float64s(worst)
	fmt.Printf("\nworst 5-minute availability: min %.4f, median %.4f\n", worst[0], worst[len(worst)/2])
	fmt.Printf("\nvalid seeds: %d of %d\n", valid, n)
	for k, v := range reasons {
		fmt.Printf("  %4d  %s\n", v, k)
	}
}
