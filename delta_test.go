package abbreviatory_test

// The 0.2 acceptance test: 500 simulated CI runs with known changes, run
// through the whole pipeline (Reduce with history, then the store's history
// file, read back for the next run), and the delta of every run compared with
// one worked out independently from the known statuses.

import (
	"fmt"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	abbreviatory "github.com/abbreviatory/engine"
	"github.com/abbreviatory/engine/store"
)

const simRuns = 500

// letters names things without digits, which grouping treats as noise.
func letters(i int) string {
	s := ""
	for {
		s = string(rune('a'+i%26)) + s
		i = i/26 - 1
		if i < 0 {
			return s
		}
	}
}

// sim holds the known status of every item in every run: 'P' passed (for
// logs: absent), 'F' failed, 'S' skipped, 'A' not in the run at all.
type sim struct {
	names  []string
	status [][]byte // [run][item], runs from 0
	tests  bool
}

func newSim(seed int64, n int, tests bool) *sim {
	rng := rand.New(rand.NewSource(seed))
	s := &sim{tests: tests}
	type behave struct {
		flaky   float64 // chance to fail each run when flaky
		broken  bool
		breakP  float64
		fixP    float64
		missing float64
	}
	bs := make([]behave, n)
	for i := range bs {
		if tests {
			s.names = append(s.names, "Test"+strings.ToUpper(letters(i)[:1])+letters(i)[1:]+"Case")
		} else {
			s.names = append(s.names, "kind"+letters(i))
		}
		b := behave{breakP: 0.02 + rng.Float64()*0.04, fixP: 0.1 + rng.Float64()*0.3}
		switch r := rng.Float64(); {
		case r < 0.15:
			b.flaky = 0.2 + rng.Float64()*0.4
		case r < 0.25:
			b.broken = true
		}
		if tests && rng.Float64() < 0.2 {
			b.missing = 0.05 + rng.Float64()*0.15
		}
		bs[i] = b
	}
	for run := 0; run < simRuns; run++ {
		row := make([]byte, n)
		// Some runs change nothing at all, so "no change" comes up.
		quiet := run > 0 && rng.Float64() < 0.15
		for i := range bs {
			b := &bs[i]
			if quiet {
				row[i] = s.status[run-1][i]
				continue
			}
			if b.broken {
				if rng.Float64() < b.fixP {
					b.broken = false
				}
			} else if rng.Float64() < b.breakP {
				b.broken = true
			}
			st := byte('P')
			if b.broken || (b.flaky > 0 && rng.Float64() < b.flaky) {
				st = 'F'
			}
			if tests && b.missing > 0 && rng.Float64() < b.missing {
				if rng.Float64() < 0.5 {
					st = 'S'
				} else {
					st = 'A'
				}
			}
			row[i] = st
		}
		s.status = append(s.status, row)
	}
	return s
}

// output writes run's output: go test -v for tests, a log otherwise. Numbers
// change from run to run, as timings and IDs do.
func (s *sim) output(run int, rng *rand.Rand) string {
	var b strings.Builder
	order := rng.Perm(len(s.names))
	if s.tests {
		for _, i := range order {
			name := s.names[i]
			switch s.status[run][i] {
			case 'A':
				continue
			case 'P':
				fmt.Fprintf(&b, "=== RUN   %s\n--- PASS: %s (0.00s)\n", name, name)
			case 'S':
				fmt.Fprintf(&b, "=== RUN   %s\n    x_test.go:%d: not on this platform\n--- SKIP: %s (0.00s)\n", name, 10+rng.Intn(90), name)
			case 'F':
				fmt.Fprintf(&b, "=== RUN   %s\n", name)
				for k := 0; k < 1+rng.Intn(4); k++ {
					fmt.Fprintf(&b, "    x_test.go:%d: got %d, want %d\n", 10+rng.Intn(90), rng.Intn(1000), rng.Intn(1000))
				}
				fmt.Fprintf(&b, "--- FAIL: %s (0.00s)\n", name)
			}
		}
		b.WriteString("FAIL\nFAIL\texample.com/sim\t0.42s\n")
		return b.String()
	}
	for k := 0; k < 20+rng.Intn(40); k++ {
		fmt.Fprintf(&b, "2026-10-18T08:%02d:%02dZ INFO served request %d in %dms\n", rng.Intn(60), rng.Intn(60), rng.Intn(100000), rng.Intn(900))
	}
	for _, i := range order {
		if s.status[run][i] != 'F' {
			continue
		}
		for k := 0; k < 1+rng.Intn(5); k++ {
			fmt.Fprintf(&b, "2026-10-18T09:%02d:%02dZ ERROR request %d failed: %s, code %d\n", rng.Intn(60), rng.Intn(60), rng.Intn(100000), s.names[i], 500+rng.Intn(4))
		}
		if rng.Float64() < 0.5 {
			fmt.Fprintf(&b, "2026-10-18T09:%02d:%02dZ WARN retrying after %dms\n", rng.Intn(60), rng.Intn(60), rng.Intn(900))
		}
	}
	return b.String()
}

// known is the status used for comparing: F, P, or '-' for unknown.
func (s *sim) known(run, i int) byte {
	switch c := s.status[run][i]; c {
	case 'F', 'P':
		return c
	}
	return '-'
}

type expected struct {
	newSet, fixed, notRun map[string]bool
	flaky                 map[string]string // name -> now
	flips                 map[string]int
	still, longest        int
	streak                map[string]int
	unchanged             bool
	failing               int
}

func (s *sim) expect(run int) expected {
	e := expected{newSet: map[string]bool{}, fixed: map[string]bool{}, notRun: map[string]bool{},
		flaky: map[string]string{}, flips: map[string]int{}, streak: map[string]int{}}
	for i, name := range s.names {
		now := s.known(run, i)
		if now == 'F' {
			e.failing++
			k := 0
			for r := run; r >= 0 && s.known(r, i) == 'F'; r-- {
				k++
			}
			e.streak[name] = k
		}
		if run == 0 {
			continue
		}
		prev := s.known(run-1, i)
		if now != 'F' && prev != 'F' {
			continue
		}
		f := 0
		var last byte
		for r := max(0, run-abbreviatory.FlakyWindow+1); r <= run; r++ {
			c := s.known(r, i)
			if c == '-' {
				continue
			}
			if last != 0 && c != last {
				f++
			}
			last = c
		}
		switch {
		case f >= abbreviatory.FlakyFlips:
			e.flaky[name] = map[byte]string{'F': "failing", 'P': "passing", '-': "not run"}[now]
			e.flips[name] = f
		case now == 'F' && prev == 'F':
			e.still++
			e.longest = max(e.longest, e.streak[name])
		case now == 'F':
			e.newSet[name] = true
		case now == 'P':
			e.fixed[name] = true
		default:
			e.notRun[name] = true
		}
	}
	if run > 0 {
		e.unchanged = true
		for i := range s.names {
			if (s.known(run, i) == 'F') != (s.known(run-1, i) == 'F') {
				e.unchanged = false
			}
		}
	}
	return e
}

var simName = regexp.MustCompile(`\b(Test[A-Za-z]+Case|kind[a-z]+)\b`)

func nameOf(t *testing.T, head string) string {
	m := simName.FindString(head)
	if m == "" {
		t.Fatalf("no simulated name in %q", head)
	}
	return m
}

func setOf(t *testing.T, items []abbreviatory.DeltaItem) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[nameOf(t, it.Head)] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	noteNew    = regexp.MustCompile(`^- (?:x\d+ )?.*\((new)\) \[ref`)
	noteStreak = regexp.MustCompile(`\((?:failing )?for (\d+) runs\) \[ref`)
)

func TestSimulatedRuns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tests bool
		n     int
		seed  int64
	}{{"go test", true, 40, 1}, {"log", false, 25, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(tc.seed, tc.n, tc.tests)
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			rng := rand.New(rand.NewSource(tc.seed + 100))
			counts := map[string]int{}
			for run := 0; run < simRuns; run++ {
				h, err := st.History("sim")
				if err != nil {
					t.Fatal(err)
				}
				budget := 600 + rng.Intn(5000)
				full := rng.Float64() < 0.2
				in := s.output(run, rng)
				res, err := abbreviatory.Reduce(strings.NewReader(in), abbreviatory.Options{Budget: budget, Track: true, History: h.Runs, Full: full})
				if err != nil {
					t.Fatalf("run %d: %v", run+1, err)
				}
				if len(res.Text) > budget {
					t.Fatalf("run %d: %d bytes over a budget of %d", run+1, len(res.Text), budget)
				}
				d, e := res.Delta, s.expect(run)
				where := fmt.Sprintf("run %d", run+1)
				if res.Run.N != run+1 || (run > 0 && d.Since != run) || (run == 0 && d.Since != 0) {
					t.Fatalf("%s: numbered %d, since %d", where, res.Run.N, d.Since)
				}
				if d.Failing != e.failing {
					t.Fatalf("%s: %d failing, want %d", where, d.Failing, e.failing)
				}
				check := func(what string, got, want map[string]bool) {
					if !sameSet(got, want) {
						t.Fatalf("%s: %s %v, want %v\n%s", where, what, keys(got), keys(want), res.Text)
					}
				}
				check("new", setOf(t, d.New), e.newSet)
				check("fixed", setOf(t, d.Fixed), e.fixed)
				check("not run", setOf(t, d.NotRun), e.notRun)
				gotFlaky := map[string]bool{}
				for _, it := range d.Flaky {
					n := nameOf(t, it.Head)
					gotFlaky[n] = true
					if it.Now != e.flaky[n] || it.Flips != e.flips[n] {
						t.Fatalf("%s: flaky %s %s with %d changes, want %s with %d", where, n, it.Now, it.Flips, e.flaky[n], e.flips[n])
					}
				}
				wantFlaky := map[string]bool{}
				for n := range e.flaky {
					wantFlaky[n] = true
				}
				check("flaky", gotFlaky, wantFlaky)
				if d.Still != e.still || d.Longest != e.longest {
					t.Fatalf("%s: %d still failing (longest %d), want %d (%d)", where, d.Still, d.Longest, e.still, e.longest)
				}
				if d.Unchanged != e.unchanged {
					t.Fatalf("%s: unchanged %v, want %v", where, d.Unchanged, e.unchanged)
				}
				for _, it := range res.Run.Bad {
					if n := nameOf(t, it.Head); it.Streak != e.streak[n] {
						t.Fatalf("%s: %s failing for %d runs, want %d", where, n, it.Streak, e.streak[n])
					}
				}
				// The report says the same as the delta.
				lines := strings.Split(strings.TrimSuffix(res.Text, "\n"), "\n")
				if d.Unchanged && !full {
					counts["one line"]++
					if len(lines) != 1 || !strings.Contains(lines[0], "since run "+strconv.Itoa(run)) && !strings.Contains(lines[0], "identical to run "+strconv.Itoa(run)) {
						t.Fatalf("%s: unchanged, but the report is:\n%s", where, res.Text)
					}
				} else {
					counts["report"]++
					for _, l := range lines {
						if noteNew.MatchString(l) {
							if n := nameOf(t, l); !e.newSet[n] {
								t.Fatalf("%s: %s marked new:\n%s", where, n, res.Text)
							}
							counts["new notes"]++
						}
						if m := noteStreak.FindStringSubmatch(l); m != nil && strings.HasPrefix(l, "- ") {
							n := nameOf(t, l)
							if k, _ := strconv.Atoi(m[1]); k != e.streak[n] {
								t.Fatalf("%s: %s noted failing for %s runs, want %d", where, n, m[1], e.streak[n])
							}
							counts["streak notes"]++
						}
					}
				}
				for k, v := range map[string]int{"new": len(d.New), "fixed": len(d.Fixed), "not run": len(d.NotRun), "flaky": len(d.Flaky), "still": d.Still} {
					counts[k] += v
				}
				if res.Run.Time = "2026-10-18T08:00:00Z"; true {
					if err := st.Record("sim", res.Run); err != nil {
						t.Fatal(err)
					}
				}
			}
			t.Logf("%d runs: %v", simRuns, counts)
			// Every kind of change came up, or the test proves little.
			for _, k := range []string{"new", "fixed", "flaky", "still", "one line", "report", "new notes", "streak notes"} {
				if counts[k] == 0 {
					t.Errorf("no %s in %d runs", k, simRuns)
				}
			}
			if tc.tests && counts["not run"] == 0 {
				t.Errorf("no not-run tests in %d runs", simRuns)
			}
			h, _ := st.History("sim")
			if len(h.Runs) != store.KeepRuns {
				t.Errorf("history keeps %d runs, want %d", len(h.Runs), store.KeepRuns)
			}
		})
	}
}
