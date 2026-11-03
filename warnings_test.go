package abbreviatory

import (
	"bytes"
	"strings"
	"testing"
)

func warnRun(t *testing.T, data []byte, warn bool, hist ...Run) *Result {
	t.Helper()
	res, err := Reduce(bytes.NewReader(data), Options{Budget: 4096, Track: true, Warnings: warn, History: hist})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// New warnings are listed, gone ones counted, and either makes the run
// changed; a warning that only moved to another line is the same one.
func TestWarningHistory(t *testing.T) {
	pad := strings.Repeat("INFO tick\n", 300)
	log := "INFO start\nWARN cache miss rate 40%\nWARN disk 81% full\nERROR db down\n" + pad
	first := warnRun(t, []byte(log), true)
	if !first.Run.Warned || len(first.Run.Warn) != 1 {
		// "cache miss rate 40%" and "disk 81% full" are different warnings.
		if len(first.Run.Warn) != 2 {
			t.Fatalf("first run warnings %v", first.Run.Warn)
		}
	}
	// The same warnings with other numbers: no change, one line.
	same := warnRun(t, []byte(strings.NewReplacer("40%", "45%", "81%", "83%").Replace(log)), true, *first.Run)
	if !same.Delta.Unchanged || len(same.Delta.NewWarnings) != 0 || strings.Count(same.Text, "\n") != 1 {
		t.Errorf("same warnings:\n%s", same.Text)
	}
	// One new, one gone.
	next := warnRun(t, []byte("INFO start\nWARN cache miss rate 40%\nWARN TLS certificate expires in 5 days\nERROR db down\n"+pad), true, *first.Run)
	d := next.Delta
	if !d.WarnCompared || len(d.NewWarnings) != 1 || len(d.GoneWarnings) != 1 || d.Unchanged {
		t.Fatalf("delta %+v\n%s", d, next.Text)
	}
	for _, want := range []string{"1 new warning, 1 warning gone", "new warning: WARN TLS certificate expires", "warning gone: WARN disk 81% full",
		"WARN TLS certificate expires in 5 days (new)"} {
		if !strings.Contains(next.Text, want) {
			t.Errorf("report lacks %q:\n%s", want, next.Text)
		}
	}
	if d.SameFailures != true {
		t.Errorf("failures changed: %+v", d)
	}
}

// Without the option, or against a run that didn't record warnings,
// warnings aren't compared.
func TestWarningHistoryOff(t *testing.T) {
	a := []byte("WARN a\nERROR x\n")
	b := []byte("WARN b\nERROR x\n")
	first := warnRun(t, a, false)
	res := warnRun(t, b, false, *first.Run)
	if !res.Delta.Unchanged || res.Delta.WarnCompared || res.Run.Warned {
		t.Errorf("warnings compared without the option: %+v", res.Delta)
	}
	res = warnRun(t, b, true, *first.Run)
	if res.Delta.WarnCompared || len(res.Delta.NewWarnings) != 0 || !res.Run.Warned {
		t.Errorf("compared with a run that didn't record warnings: %+v", res.Delta)
	}
	// Trimmed old runs drop their warnings.
	r := *res.Run
	r.Trim()
	if r.Warn != nil {
		t.Error("Trim kept warnings")
	}
}

// With -preserve leaving warnings out, there is nothing to compare.
func TestWarningHistoryPreserve(t *testing.T) {
	a := []byte("WARN a\nERROR x\n")
	first, _ := Reduce(bytes.NewReader(a), Options{Track: true, Warnings: true, Preserve: []string{"errors", "failures"}})
	if first.Run.Warned {
		t.Error("warnings recorded although not preserved")
	}
}

// Build tools: changing one compiler warning reports it, not the others.
func TestWarningHistoryCargo(t *testing.T) {
	data := readFixture(t, "cargo-build.txt")
	first := warnRun(t, data, true)
	next := warnRun(t, bytes.Replace(data, []byte("unused import: `std::collections::HashMap`"), []byte("unused import: `std::collections::BTreeMap`"), 1), true, *first.Run)
	if len(next.Delta.NewWarnings) != 1 || len(next.Delta.GoneWarnings) != 1 || len(next.Delta.New) != 0 {
		t.Errorf("delta new warnings %v, gone %v, new %v", next.Delta.NewWarnings, next.Delta.GoneWarnings, next.Delta.New)
	}
}
