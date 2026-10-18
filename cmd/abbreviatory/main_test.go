package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
		{[]string{"reduce", "-store", dir, "-budget", "10"}, 1, "too small"},
		{[]string{"reduce", "-store", dir, "-format", "yaml"}, 1, "unknown format"},
		{[]string{"reduce", "-store", dir, "-keep", "("}, 2, "-keep"},
		{[]string{"reduce", "-store", dir, "-preserve", "bugs"}, 1, "unknown preserve"},
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
