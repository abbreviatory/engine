package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func runCLI(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), code
}

var refRE = regexp.MustCompile(`\[ref ([0-9a-f]{12}):(g\d+)\]`)

func sample() string {
	return strings.Repeat("2026-10-18T08:00:00Z INFO tick\n", 500) +
		"2026-10-18T08:00:01Z ERROR payment failed for order 77\n" +
		strings.Repeat("2026-10-18T08:00:02Z WARN slow query 900ms\n", 40)
}

func TestReduceAndExpand(t *testing.T) {
	store := t.TempDir()
	out, errs, code := runCLI(t, sample(), "reduce", "-store", store)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	if !strings.Contains(out, "ERROR payment failed for order 77") || !strings.Contains(out, "x40") {
		t.Fatalf("unexpected output:\n%s", out)
	}
	m := refRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no reference in:\n%s", out)
	}
	exp, errs, code := runCLI(t, "", "expand", "-store", store, m[1]+":"+m[2])
	if code != 0 || exp != "2026-10-18T08:00:01Z ERROR payment failed for order 77\n" {
		t.Fatalf("expand: exit %d, %q, %s", code, exp, errs)
	}
	whole, _, code := runCLI(t, "", "expand", "-store", store, m[1])
	if code != 0 || whole != sample() {
		t.Fatal("whole input doesn't expand back")
	}
}

func TestReduceFileAndJSON(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "in.log")
	os.WriteFile(f, []byte(sample()), 0o600)
	out, errs, code := runCLI(t, "", "reduce", "-store", dir, "-json", "-budget", "1000", f)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	var v struct {
		Format     string           `json:"format"`
		Ref        string           `json:"ref"`
		InputLines int64            `json:"input_lines"`
		Text       string           `json:"text"`
		Lines      map[string]int64 `json:"lines"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Format != "logs" || len(v.Ref) != 12 || v.InputLines != 541 || len(v.Text) > 1000 || v.Lines["warn"] != 40 {
		t.Errorf("%+v", v)
	}
}

func TestNoStore(t *testing.T) {
	dir := t.TempDir()
	out, _, code := runCLI(t, sample(), "reduce", "-no-store", "-store", dir)
	if code != 0 || strings.Contains(out, "[ref") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	ents, _ := os.ReadDir(filepath.Join(dir, "objects"))
	if len(ents) != 0 {
		t.Errorf("-no-store stored %d files", len(ents))
	}
}

func TestErrors(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"reduce", "-store", dir, "-budget", "10"}, 2, "at least 512"},
		{[]string{"reduce", "-store", dir, "-budget", "0"}, 2, "at least 512"},
		{[]string{"reduce", "-store", dir, "-format", "yaml"}, 2, "unknown format"},
		{[]string{"reduce", "-store", dir, "-keep", "("}, 2, "-keep"},
		{[]string{"reduce", "-store", dir, "-preserve", "bugs"}, 2, "unknown preserve"},
		{[]string{"reduce", "-store", dir, filepath.Join(dir, "missing")}, 1, "no such file"},
		{[]string{"expand", "-store", dir, "nonsense"}, 1, "not a reference"},
		{[]string{"expand", "-store", dir, "0123456789ab:g0"}, 1, "not in the store"},
		{[]string{"frobnicate"}, 2, "unknown command"},
		{nil, 2, "Usage"},
	} {
		_, errs, code := runCLI(t, sample(), c.args...)
		if code != c.code || !strings.Contains(errs, c.want) {
			t.Errorf("%v: exit %d, %q", c.args, code, errs)
		}
	}
}

func TestVersionFormatsPrune(t *testing.T) {
	out, _, code := runCLI(t, "", "version")
	if code != 0 || !strings.HasPrefix(out, "abbreviatory ") {
		t.Errorf("version: %q", out)
	}
	out, _, _ = runCLI(t, "", "formats")
	for _, f := range []string{"gotest-json", "pytest", "junit", "logs", "json"} {
		if !strings.Contains(out, f) {
			t.Errorf("formats lacks %s", f)
		}
	}
	dir := t.TempDir()
	runCLI(t, sample(), "reduce", "-store", dir)
	out, _, code = runCLI(t, "", "prune", "-store", dir, "-max-size", "0")
	if code != 0 || !strings.HasPrefix(out, "removed 1 stored originals") {
		t.Errorf("prune: %d %q", code, out)
	}
	if _, errs, code := runCLI(t, "", "prune", "-store", dir, "-max-size", "lots"); code != 2 || !strings.Contains(errs, "max-size") {
		t.Errorf("bad size: %d %q", code, errs)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"500MB": 500 << 20, "1gb": 1 << 30, "10 KB": 10 << 10, "7": 7, "0": 0} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
}

func TestReduceWithKeyAndHistory(t *testing.T) {
	store := t.TempDir()
	first := "2026-10-18T08:00:00Z INFO up\n2026-10-18T08:00:01Z ERROR disk full\n"
	second := "2026-10-18T09:00:00Z INFO up\n2026-10-18T09:00:01Z ERROR cache miss storm\n"
	out, errs, code := runCLI(t, first, "reduce", "-store", store, "-key", "nightly")
	if code != 0 || !strings.Contains(out, "run 1; no earlier runs") {
		t.Fatalf("exit %d: %s%s", code, out, errs)
	}
	out, _, _ = runCLI(t, second, "reduce", "-store", store, "-key", "nightly")
	if !strings.Contains(out, "run 2; since run 1: 1 new, 1 gone") || !strings.Contains(out, "new: 2026-10-18T09:00:01Z ERROR cache miss storm") {
		t.Fatalf("second run:\n%s", out)
	}
	// The gone error's reference points into run 1's original.
	m := regexp.MustCompile(`gone: .*\[ref ([0-9a-f]{12}:g\d+)\]`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no reference on the gone line:\n%s", out)
	}
	exp, _, code := runCLI(t, "", "expand", "-store", store, m[1])
	if code != 0 || exp != "2026-10-18T08:00:01Z ERROR disk full\n" {
		t.Fatalf("expand %s: %q", m[1], exp)
	}
	out, _, _ = runCLI(t, second, "reduce", "-store", store, "-key", "nightly", "-json")
	var v struct {
		Run   int `json:"run"`
		Delta struct {
			Since     int  `json:"since"`
			Identical bool `json:"identical"`
			Unchanged bool `json:"unchanged"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Run != 3 || v.Delta.Since != 2 || !v.Delta.Identical || !v.Delta.Unchanged {
		t.Fatalf("json: %v %+v\n%s", err, v, out)
	}

	out, _, code = runCLI(t, "", "history", "-store", store)
	if code != 0 || !strings.Contains(out, "nightly\n    run 3, ") {
		t.Fatalf("history list:\n%s", out)
	}
	out, _, code = runCLI(t, "", "history", "-store", store, "night")
	if code != 0 || !strings.Contains(out, "run 2  ") || !strings.Contains(out, "(1 new, 1 fixed)") {
		t.Fatalf("history nightly:\n%s", out)
	}
	runCLI(t, first, "reduce", "-store", store, "-key", "nightly-arm")
	_, errs, code = runCLI(t, "", "history", "-store", store, "night")
	if code != 1 || !strings.Contains(errs, "matches 2 histories") {
		t.Fatalf("ambiguous key: exit %d %s", code, errs)
	}
	if _, _, code = runCLI(t, "", "history", "-store", store, "nightly"); code != 0 {
		t.Fatal("an exact key should win over partial matches")
	}
	out, _, _ = runCLI(t, "", "history", "-store", store, "-json", "nightly")
	if !strings.Contains(out, `"key": "nightly"`) || !strings.Contains(out, `"run": 3`) {
		t.Fatalf("history json:\n%s", out)
	}
}

func TestRunKeepsExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	store := t.TempDir()
	script := `echo "2026-10-18T08:00:00Z INFO start"; echo "2026-10-18T08:00:01Z ERROR boom" >&2; exit 3`
	out, errs, code := runCLI(t, "", "run", "-store", store, "-key", "job", "--", "sh", "-c", script)
	if code != 3 {
		t.Fatalf("exit %d, want 3: %s", code, errs)
	}
	if !strings.Contains(out, "run 1, exit status 3; no earlier runs") || !strings.Contains(out, "ERROR boom") {
		t.Fatalf("output:\n%s", out)
	}
	out, _, code = runCLI(t, "", "run", "-store", store, "-key", "job", "--", "sh", "-c", script)
	if code != 3 || strings.Count(out, "\n") != 1 || !strings.Contains(out, "run 2, exit status 3, identical to run 1: the same 1 error") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
	out, _, code = runCLI(t, "", "run", "-store", store, "-no-history", "--", "sh", "-c", "echo fine")
	if code != 0 || strings.Contains(out, "run 1") || !strings.Contains(out, "fine") {
		t.Fatalf("no history: exit %d\n%s", code, out)
	}
	_, errs, code = runCLI(t, "", "run", "-store", store, "--", "no-such-command-abbreviatory")
	if code != 127 || !strings.Contains(errs, "cannot run") {
		t.Fatalf("missing command: exit %d %s", code, errs)
	}
	if _, _, code = runCLI(t, "", "run", "-store", store); code != 2 {
		t.Fatalf("no command: exit %d", code)
	}
	// The default key is the project and the command.
	runCLI(t, "", "run", "-store", store, "--", "sh", "-c", "echo x ERROR a")
	out, _, _ = runCLI(t, "", "history", "-store", store)
	if !strings.Contains(out, ": sh -c echo x ERROR a") {
		t.Fatalf("default key not listed:\n%s", out)
	}
}
