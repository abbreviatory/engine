package abbreviatory

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

var anyRef = regexp.MustCompile(`\[ref ([0-9a-f]{12})(?::([^\]]+))?\]`)

// changed returns a different run of the same fixture: every third line
// gone, so some failures and errors disappear and the counts move.
func changed(data []byte) []byte {
	var out []byte
	for i, l := range splitKeep(data) {
		if i%3 != 2 {
			out = append(out, l...)
		}
	}
	return out
}

// With history the promise still holds: the budget, the same bytes for the
// same input and history, and every reference expanding, the previous run's
// included.
func TestTrackedPromise(t *testing.T) {
	for name, data := range fixtures(t) {
		other := changed(data)
		if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".xml") {
			// Cutting lines breaks these documents; a different run is the
			// same document with its first error value changed.
			other = bytes.Replace(data, []byte(`"error"`), []byte(`"Error"`), 1)
		}
		budgets := []int{512, 1024, 4096, 16384}
		if testing.Short() || raceEnabled {
			budgets = []int{1024}
		}
		for _, budget := range budgets {
			for _, order := range [][2][]byte{{data, other}, {other, data}} {
				first, err := Reduce(bytes.NewReader(order[0]), Options{Budget: 16384, Track: true})
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				for _, full := range []bool{false, true} {
					opt := Options{Budget: budget, Track: true, History: []Run{*first.Run}, Full: full}
					res, err := Reduce(bytes.NewReader(order[1]), opt)
					if be, ok := err.(*BudgetError); ok {
						if budget > 1024 {
							t.Fatalf("%s at %d: %v", name, budget, be)
						}
						continue
					}
					if err != nil {
						t.Fatalf("%s at %d: %v", name, budget, err)
					}
					if len(res.Text) > budget {
						t.Fatalf("%s at %d: %d bytes", name, budget, len(res.Text))
					}
					again, _ := Reduce(bytes.NewReader(order[1]), opt)
					if again.Text != res.Text {
						t.Fatalf("%s at %d: not deterministic", name, budget)
					}
					if !strings.Contains(res.Text, "since run 1") && !strings.Contains(res.Text, "identical to run 1") {
						t.Fatalf("%s at %d: no comparison in:\n%s", name, budget, res.Text)
					}
					for _, m := range anyRef.FindAllStringSubmatch(res.Text, -1) {
						src, ix := order[1], res.Index
						if m[1] == first.ID && first.ID != res.ID {
							src, ix = order[0], first.Index
						} else if m[1] != res.ID {
							t.Fatalf("%s: reference to unknown %s", name, m[1])
						}
						part, err := Expand(src, ix, m[2])
						if err != nil || len(part) == 0 {
							t.Fatalf("%s at %d: %s:%s doesn't expand: %v", name, budget, m[1], m[2], err)
						}
					}
				}
			}
		}
	}
}

func TestFirstRunAndNoChange(t *testing.T) {
	log := "2026-10-18T08:00:00Z INFO start\n2026-10-18T08:00:01Z ERROR disk full on /var/data\n"
	first, err := Reduce(strings.NewReader(log), Options{Track: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Text, "run 1; no earlier runs to compare with (1 failure recorded)") {
		t.Fatalf("first run:\n%s", first.Text)
	}
	// Different timestamps, same events: no change, one line.
	again := strings.ReplaceAll(log, "08:00", "09:30")
	res, err := Reduce(strings.NewReader(again), Options{Track: true, History: []Run{*first.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(res.Text, "\n") != 1 || !strings.Contains(res.Text, "run 2, no change since run 1: the same 1 error") {
		t.Fatalf("unchanged run:\n%s", res.Text)
	}
	same, _ := Reduce(strings.NewReader(log), Options{Track: true, History: []Run{*first.Run}})
	if !strings.Contains(same.Text, "identical to run 1") {
		t.Fatalf("identical run:\n%s", same.Text)
	}
	if res.Run.Bad[groupKeyOfLog(t, res)].Streak != 2 {
		t.Fatal("streak not carried")
	}
	exit := 2
	fixed, _ := Reduce(strings.NewReader("2026-10-18T08:00:00Z INFO start\n"), Options{Track: true, History: []Run{*first.Run, *res.Run}, Exit: &exit})
	if !strings.Contains(fixed.Text, "run 3, exit status 2; since run 2: 1 gone") || !strings.Contains(fixed.Text, "gone: 2026-10-18T09:30:01Z ERROR disk full") {
		t.Fatalf("fixed run:\n%s", fixed.Text)
	}
}

func groupKeyOfLog(t *testing.T, res *Result) string {
	for k := range res.Run.Bad {
		return k
	}
	t.Fatal("no failures recorded")
	return ""
}

// A run trimmed of its failures (older than the flaky window) can't be
// compared with: the next run is treated as the first.
func TestTrimmedHistory(t *testing.T) {
	first, _ := Reduce(strings.NewReader("x ERROR boom\n"), Options{Track: true})
	old := *first.Run
	old.Trim()
	res, err := Reduce(strings.NewReader("x ERROR boom\n"), Options{Track: true, History: []Run{old}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Run.N != 2 || res.Delta.Since != 0 || !strings.Contains(res.Text, "no earlier runs to compare with") {
		t.Fatalf("got run %d since %d:\n%s", res.Run.N, res.Delta.Since, res.Text)
	}
}

// Without -no-store the report has references; with it, none, history or
// not.
func TestTrackedNoRefs(t *testing.T) {
	a, _ := Reduce(strings.NewReader("x ERROR one\n"), Options{Track: true, NoRefs: true})
	b, err := Reduce(strings.NewReader("x ERROR two\nx ERROR three\n"), Options{Track: true, NoRefs: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Text, "[ref") || !strings.Contains(b.Text, "new: x ERROR two") || !strings.Contains(b.Text, "gone: x ERROR one") {
		t.Fatalf("got:\n%s", b.Text)
	}
}

func TestGoodPacking(t *testing.T) {
	good := map[string]bool{hashKey("a"): true, hashKey("b"): true}
	back := unpackGood(packGood(good))
	if len(back) != 2 || !back[hashKey("a")] || !back[hashKey("b")] {
		t.Fatal("passing tests don't survive packing")
	}
	if flips([]byte("PF-FPF")) != 3 || flips([]byte("--F--")) != 0 {
		t.Fatal("flips miscounted")
	}
}

// Review of 0.2, finding 1: go test without -v names no passing test, so a
// failing test that disappears passed.
func TestPlainGoTestFixed(t *testing.T) {
	run1 := "--- FAIL: TestAlpha (0.00s)\n    a_test.go:9: bad\n--- FAIL: TestBeta (0.00s)\n    a_test.go:19: bad\nFAIL\nFAIL\texample.com/a\t0.01s\n"
	run2 := "--- FAIL: TestBeta (0.00s)\n    a_test.go:19: bad\nFAIL\nFAIL\texample.com/a\t0.01s\n"
	a, _ := Reduce(strings.NewReader(run1), Options{Track: true})
	b, err := Reduce(strings.NewReader(run2), Options{Track: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Text, "since run 1: 1 fixed, 1 still failing") || !strings.Contains(b.Text, "fixed: TestAlpha (example.com/a, 0.00s)") {
		t.Fatalf("got:\n%s", b.Text)
	}
}

// Review of 0.2, finding 2: the same test name in two packages is two tests.
func TestSameNameTwoPackages(t *testing.T) {
	run1 := "=== RUN   TestOpen\n--- FAIL: TestOpen (0.00s)\nFAIL\nFAIL\texample.com/a\t0.01s\n=== RUN   TestOpen\n--- PASS: TestOpen (0.00s)\nPASS\nok  \texample.com/b\t0.01s\n"
	run2 := "=== RUN   TestOpen\n--- PASS: TestOpen (0.00s)\nPASS\nok  \texample.com/a\t0.01s\n=== RUN   TestOpen\n    b_test.go:5: other\n--- FAIL: TestOpen (0.00s)\nFAIL\nFAIL\texample.com/b\t0.01s\n"
	a, _ := Reduce(strings.NewReader(run1), Options{Track: true})
	b, err := Reduce(strings.NewReader(run2), Options{Track: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Text, "1 new, 1 fixed") || !strings.Contains(b.Text, "new: TestOpen (example.com/b, 0.00s)") {
		t.Fatalf("got:\n%s", b.Text)
	}
}

// Review of 0.2, second pass, A: a -json or -v run where nothing passed (a
// broken build, all skipped) doesn't make missing tests fixed.
func TestNoPassesIsNotFixed(t *testing.T) {
	run1 := `{"Action":"run","Package":"example.com/a","Test":"TestA"}
{"Action":"output","Package":"example.com/a","Test":"TestA","Output":"    a_test.go:9: bad\n"}
{"Action":"fail","Package":"example.com/a","Test":"TestA","Elapsed":0}
{"Action":"fail","Package":"example.com/a","Elapsed":0.1}
`
	run2 := `{"ImportPath":"example.com/a","Action":"build-output","Output":"a.go:3:1: syntax error\n"}
{"ImportPath":"example.com/a","Action":"build-fail"}
{"Action":"fail","Package":"example.com/a","Elapsed":0,"FailedBuild":"example.com/a"}
`
	a, _ := Reduce(strings.NewReader(run1), Options{Track: true})
	b, err := Reduce(strings.NewReader(run2), Options{Track: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Text, "fixed") || !strings.Contains(b.Text, "not run: TestA") {
		t.Fatalf("got:\n%s", b.Text)
	}
	v1 := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\n=== RUN   TestC\n--- FAIL: TestC (0.00s)\nFAIL\nFAIL\texample.com/a\t0.01s\n"
	v2 := "=== RUN   TestB\n--- FAIL: TestB (0.00s)\n=== RUN   TestC\n--- SKIP: TestC (0.00s)\nFAIL\nFAIL\texample.com/a\t0.01s\n"
	a, _ = Reduce(strings.NewReader(v1), Options{Track: true})
	b, _ = Reduce(strings.NewReader(v2), Options{Track: true, History: []Run{*a.Run}})
	if strings.Contains(b.Text, "fixed") || !strings.Contains(b.Text, "not run: TestC") {
		t.Fatalf("skipped test:\n%s", b.Text)
	}
}

// Review of 0.2, second pass, B: a run cut short before its package line
// still matches its tests with the next run's.
func TestCutShortRunMatches(t *testing.T) {
	run1 := "=== RUN   TestB\n    b_test.go:5: bad\n--- FAIL: TestB (0.00s)\n=== RUN   TestC\n"
	run2 := "=== RUN   TestB\n    b_test.go:5: bad\n--- FAIL: TestB (0.00s)\n=== RUN   TestC\n--- PASS: TestC (0.00s)\nFAIL\nFAIL\texample.com/x\t0.01s\n"
	a, _ := Reduce(strings.NewReader(run1), Options{Track: true})
	b, err := Reduce(strings.NewReader(run2), Options{Track: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Text, "since run 1: 1 fixed, 1 still failing (longest for 2 runs)") || !strings.Contains(b.Text, "fixed: TestC (no result: the run stopped)") {
		t.Fatalf("got:\n%s", b.Text)
	}
	// And the other way round: a qualified run, then one cut short.
	c, _ := Reduce(strings.NewReader(run1), Options{Track: true, History: []Run{*a.Run, *b.Run}})
	if !strings.Contains(c.Text, "1 new, 1 still failing (longest for 3 runs)") {
		t.Fatalf("cut short after a full run:\n%s", c.Text)
	}
}

// Review of 0.2, third pass: in plain go test output, a missing test of a
// package that failed to build didn't pass.
func TestBuildFailedPackageNotFixed(t *testing.T) {
	run1 := "ok  \texample.com/a\t0.01s\n--- FAIL: TestX (0.00s)\n    x_test.go:1: bad\nFAIL\nFAIL\texample.com/b\t0.01s\n--- FAIL: TestY (0.00s)\n    y_test.go:1: bad\nFAIL\nFAIL\texample.com/c\t0.01s\n"
	run2 := "ok  \texample.com/a\t0.01s\n# example.com/b\nb.go:3:1: syntax error\nFAIL\texample.com/b [build failed]\n--- FAIL: TestY (0.00s)\n    y_test.go:1: bad\nFAIL\nFAIL\texample.com/c\t0.01s\n"
	a, _ := Reduce(strings.NewReader(run1), Options{Track: true})
	b, err := Reduce(strings.NewReader(run2), Options{Track: true, History: []Run{*a.Run}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Text, "fixed") || !strings.Contains(b.Text, "not run: TestX (example.com/b, 0.00s)") {
		t.Fatalf("got:\n%s", b.Text)
	}
}

// A run cut short pairs a failing name shared by two packages with one of
// them, instead of calling it new.
func TestCutShortSharedName(t *testing.T) {
	full := "=== RUN   TestB\n--- FAIL: TestB (0.00s)\nFAIL\nFAIL\texample.com/a\t0.01s\n=== RUN   TestB\n--- FAIL: TestB (0.00s)\nFAIL\nFAIL\texample.com/b\t0.01s\n"
	cut := "=== RUN   TestB\n--- FAIL: TestB (0.00s)\n"
	a, _ := Reduce(strings.NewReader(full), Options{Track: true})
	b, _ := Reduce(strings.NewReader(cut), Options{Track: true, History: []Run{*a.Run}})
	if strings.Contains(b.Text, "new") || !strings.Contains(b.Text, "1 not run, 1 still failing") {
		t.Fatalf("cut short:\n%s", b.Text)
	}
	c, _ := Reduce(strings.NewReader(full), Options{Track: true, History: []Run{*a.Run, *b.Run}})
	if !strings.Contains(c.Text, "1 new, 1 still failing") && !strings.Contains(c.Text, "2 still failing") {
		t.Fatalf("full again:\n%s", c.Text)
	}
}
