package abbreviatory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the independent review of the build-tool adapters
// before v0.5.0. Inputs are in testdata/review05, most of them real output.

func review05(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "review05", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func review05Reduce(t *testing.T, data []byte, opt Options) *Result {
	t.Helper()
	res, err := Reduce(bytes.NewReader(data), opt)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestReview05Promise(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "review05", "*"))
	if len(files) == 0 {
		t.Fatal("no review inputs")
	}
	for _, f := range files {
		name := filepath.Base(f)
		data := review05(t, name)
		checkCoverage(t, name, data, review05Reduce(t, data, Options{}))
		for _, b := range []int{512, 1000, 4096} {
			checkPromise(t, name, data, Options{Budget: b})
		}
	}
}

// 1. Coloured output (CARGO_TERM_COLOR=always, eslint --color) is detected.
func TestReview05Colour(t *testing.T) {
	for name, want := range map[string]string{"cargo-color.txt": "cargo", "eslint-color.txt": "eslint"} {
		for _, f := range []string{"", "build"} {
			res := review05Reduce(t, review05(t, name), Options{Format: f})
			if res.Format != want {
				t.Errorf("%s -format %q: read as %s", name, f, res.Format)
			}
		}
	}
	res := review05Reduce(t, review05(t, "eslint-color.txt"), Options{Budget: 1500})
	if res.Records["error"] != 29 || res.Records["warn"] != 25 {
		t.Errorf("eslint --color: %v", res.Records)
	}
	res = review05Reduce(t, review05(t, "cargo-color.txt"), Options{Budget: 1500})
	if !strings.Contains(res.Text, "at src/main.rs:12:27") {
		t.Errorf("cargo --color: heads lack the location:\n%s", res.Text)
	}
}

// 2. A log with many "[INFO]" lines is a log, and its traceback one error.
func TestReview05InfoLogNotMaven(t *testing.T) {
	res := review05Reduce(t, review05(t, "infolog.txt"), Options{Budget: 600})
	if res.Format != "logs" {
		t.Fatalf("read as %s", res.Format)
	}
	if !strings.Contains(res.Text, "ZeroDivisionError") || res.Records["error"] != 1 {
		t.Errorf("traceback not kept with its error: %v\n%s", res.Records, res.Text)
	}
}

// 3 and 4. tsc --watch starts with "[": it's tsc, and each compilation's
// count ends the diagnostic before it.
func TestReview05TscWatch(t *testing.T) {
	res := review05Reduce(t, review05(t, "tscw2.txt"), Options{Budget: 1200})
	if res.Format != "tsc" {
		t.Fatalf("read as %s", res.Format)
	}
	if res.Records["error"] > 12 {
		t.Errorf("watch output swallowed into a diagnostic: %v", res.Records)
	}
	if !strings.Contains(res.Text, "runner: [10:01:01 AM] Found 0 errors") && !strings.Contains(res.Text, "Found 0 errors. Watching") {
		t.Errorf("last count not the runner line:\n%s", res.Text)
	}
	if strings.Count(res.Text, "runner:") != 1 {
		t.Errorf("one runner line wanted:\n%s", res.Text)
	}
}

// 5. Two crates of a workspace with the same test name: the failure is the
// failing crate's, with its panic message.
func TestReview05CargoWorkspace(t *testing.T) {
	res := review05Reduce(t, review05(t, "ws-test.txt"), Options{Budget: 1000})
	if strings.Contains(res.Text, "(again") || !strings.Contains(res.Text, "tests::it_works - assertion `left == right` failed: b is broken") {
		t.Errorf("failure on the wrong test:\n%s", res.Text)
	}
	if res.Records["pass"] != 2 || res.Records["fail"] != 1 {
		t.Errorf("counts %v", res.Records)
	}
	if got := cargoTarget("Running unittests src/lib.rs (target/debug/deps/b-b330f3dc66b5b7aa)"); got != "b unittests src/lib.rs" {
		t.Errorf("target %q", got)
	}
	if got := cargoTarget(`Running tests\api.rs (target\debug\deps\api-0123abcd.exe)`); got != `api tests\api.rs` {
		t.Errorf("windows target %q", got)
	}
}

// 6. A Maven or Gradle test failing with another message is the same test,
// still failing; one fixed is fixed, not "not run".
func TestReview05BuildTestHistory(t *testing.T) {
	first := review05Reduce(t, readFixture(t, "maven-test.txt"), Options{Track: true})
	res := review05Reduce(t, review05(t, "mt2.txt"), Options{Track: true, History: []Run{*first.Run}})
	if len(res.Delta.New) != 0 || len(res.Delta.Fixed) != 0 {
		t.Errorf("maven: new %v, fixed %v", res.Delta.New, res.Delta.Fixed)
	}
	// roundsHalfUp passes in the next run: fixed.
	fixed := bytes.ReplaceAll(readFixture(t, "maven-test.txt"), []byte("shop.MoneyTest.roundsHalfUp -- Time elapsed: 0.001 s <<< FAILURE!"), []byte("shop.MoneyTest.roundsHalfUp done"))
	res = review05Reduce(t, fixed, Options{Track: true, History: []Run{*first.Run}})
	if len(res.Delta.Fixed) != 1 || len(res.Delta.NotRun) != 0 {
		t.Errorf("maven fixed: fixed %v, not run %v", res.Delta.Fixed, res.Delta.NotRun)
	}
	g := readFixture(t, "gradle-test.txt")
	first = review05Reduce(t, g, Options{Track: true})
	res = review05Reduce(t, bytes.ReplaceAll(g, []byte("expected:<20> but was:<10>"), []byte("expected:<20> but was:<11>")), Options{Track: true, History: []Run{*first.Run}})
	if len(res.Delta.New) != 0 {
		t.Errorf("gradle: new %v", res.Delta.New)
	}
}

// 7. "What went wrong" and "Failed to execute goal" keep their identity
// when an error under them changes.
func TestReview05UmbrellaIdentity(t *testing.T) {
	first := review05Reduce(t, readFixture(t, "gradle-compile.txt"), Options{Track: true})
	res := review05Reduce(t, review05(t, "gc2.txt"), Options{Track: true, History: []Run{*first.Run}})
	for _, it := range append(res.Delta.New, res.Delta.Fixed...) {
		if strings.Contains(it.Head, "Execution failed") {
			t.Errorf("gradle umbrella changed: %v / %v", res.Delta.New, res.Delta.Fixed)
		}
	}
	m := readFixture(t, "maven-compile.txt")
	first = review05Reduce(t, m, Options{Track: true})
	res = review05Reduce(t, bytes.ReplaceAll(m, []byte("method totl()"), []byte("method tot()")), Options{Track: true, History: []Run{*first.Run}})
	for _, it := range append(res.Delta.New, res.Delta.Fixed...) {
		if strings.Contains(it.Head, "Failed to execute goal") {
			t.Errorf("maven umbrella changed: %v / %v", res.Delta.New, res.Delta.Fixed)
		}
	}
}

// 8 and 9. Each "Failed to execute goal" is its own error; the resume hint
// is information.
func TestReview05MavenGoals(t *testing.T) {
	res := review05Reduce(t, review05(t, "mvn3.txt"), Options{Budget: 1200})
	if !strings.Contains(res.Text, "on project core") || !strings.Contains(res.Text, "- Failed to execute goal org.apache.maven.plugins:maven-compiler-plugin:3.13.0:compile (default-compile) on project web") {
		t.Errorf("second goal absorbed:\n%s", res.Text)
	}
	res = review05Reduce(t, review05(t, "mvnrf.txt"), Options{Budget: 512})
	if res.Records["error"] > 3 {
		t.Errorf("resume hint counted as an error: %v\n%s", res.Records, res.Text)
	}
}

// 10. Kotlin before 1.8.
func TestReview05KotlinOld(t *testing.T) {
	res := review05Reduce(t, review05(t, "kt2.txt"), Options{Budget: 800})
	if !strings.Contains(res.Text, "Shop.kt:12:5 error: Unresolved reference: totl") || res.Records["warn"] != 1 {
		t.Errorf("kotlin 1.x: %v\n%s", res.Records, res.Text)
	}
}

// 11. cargo test holds only its failures: passes are emitted as they come.
func TestReview05CargoPassesStream(t *testing.T) {
	c := newCargoTest()
	n := 0
	emit := func(r Record) { n++ }
	c.line(1, "     Running unittests src/lib.rs (target/debug/deps/a-0123abcd)", emit)
	for i := 0; i < 1000; i++ {
		c.line(int64(i+2), fmt.Sprintf("test t%d ... ok", i), emit)
	}
	if len(c.tests) != 0 || n < 1000 {
		t.Errorf("%d tests buffered, %d emitted", len(c.tests), n)
	}
}

// Minor: the panic message of Rust before 1.73; Gradle's head not repeated
// in its detail; Maven says "warning"; extensionless ESLint paths.
func TestReview05Minor(t *testing.T) {
	if got := cargoPanicMessage([]string{"thread 'x' panicked at 'boom', src/lib.rs:4:5", "note: run with `RUST_BACKTRACE=1`"}); got != "boom" {
		t.Errorf("old panic: %q", got)
	}
	res := review05Reduce(t, readFixture(t, "gradle-test.txt"), Options{Budget: 4096})
	if strings.Count(res.Text, "Execution failed for task ':test'.") > 1 {
		t.Errorf("head repeated in detail:\n%s", res.Text)
	}
	res = review05Reduce(t, readFixture(t, "maven-compile.txt"), Options{Budget: 4096})
	if strings.Contains(res.Text, " warn: ") || !strings.Contains(res.Text, "Cart.java:7:19 warning: found raw type") {
		t.Errorf("maven warning wording:\n%s", res.Text)
	}
	e := newEslint()
	var heads []string
	for i, l := range []string{"/work/bin/cli", "  3:1  error  'x' is not defined  no-undef"} {
		e.line(int64(i+1), l, func(r Record) { heads = append(heads, r.Head) })
	}
	if len(heads) != 2 || heads[1] != "/work/bin/cli:3:1 error 'x' is not defined (no-undef)" {
		t.Errorf("extensionless path: %q", heads)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("test", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
