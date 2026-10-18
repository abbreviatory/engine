package abbreviatory

// Regression tests for the findings of the independent review before
// v0.1.0. Each names the finding it covers.

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestReviewLogStartingWithBracket(t *testing.T) { // H1
	for _, in := range []string{
		"[INFO] Scanning for projects...\n[INFO] Building app 1.0\n[ERROR] Failed to execute goal compile\n",
		"[2026-10-18 08:00:00] INFO start\n[2026-10-18 08:00:01] ERROR db down\n",
		"[\"a\",1]\n[\"b\",2]\n",
		"{\"level\":\"info\",\"msg\":\"a\"}\n\n{\"level\":\"error\",\"msg\":\"b\"}\n",
	} {
		res := reduceString(t, in, Options{})
		if res.Format != "logs" {
			t.Errorf("%q: format %s", in, res.Format)
		}
		checkCoverage(t, "bracket", []byte(in), res)
	}
	res := reduceString(t, "[INFO] a\n[ERROR] Failed to execute goal\n"+strings.Repeat("[INFO] x\n", 300), Options{})
	if !strings.Contains(res.Text, "ERRORS") || !strings.Contains(res.Text, "Failed to execute goal") {
		t.Errorf("Maven error not kept:\n%s", res.Text)
	}
}

func TestReviewKeepRulesChangeTheID(t *testing.T) { // H2
	in := "INFO tick 1 acme\n" + strings.Repeat("INFO tick 5\n", 300) + "INFO tick 7\nERROR disk full\n"
	a := reduceString(t, in, Options{})
	b := reduceString(t, in, Options{Keep: []*regexp.Regexp{regexp.MustCompile(`tick 7$`)}})
	if a.ID == b.ID {
		t.Fatal("keep rules don't change the reference ID")
	}
	c := reduceString(t, in, Options{Keep: []*regexp.Regexp{regexp.MustCompile(`tick 7$`)}})
	if b.ID != c.ID {
		t.Error("same keep rules, different ID")
	}
}

func TestReviewHugePytestCount(t *testing.T) { // H3
	in := "============================= test session starts ==============================\n" +
		"======== 2000000000 passed in 1.00s ========\n"
	start := time.Now()
	res := reduceString(t, in, Options{})
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if res.Records["pass"] != 2000000000 {
		t.Errorf("pass records %d", res.Records["pass"])
	}
}

func TestReviewPytestStrayErrors(t *testing.T) { // H4
	in := "============================= test session starts ==============================\n" +
		"ERROR: Could not find a version that satisfies the requirement foo==9.9\n" +
		strings.Repeat("collected 3 items\n", 50) +
		"INTERNALERROR> Traceback (most recent call last):\n" +
		"INTERNALERROR>   File \"x.py\", line 1, in f\n" +
		"INTERNALERROR> RuntimeError: plugin crashed\n"
	res := reduceString(t, in, Options{Budget: 600})
	if res.Records["error"] != 2 {
		t.Errorf("errors %d:\n%s", res.Records["error"], res.Text)
	}
	if !strings.Contains(res.Text, "plugin crashed") {
		t.Errorf("INTERNALERROR not shown:\n%s", res.Text)
	}
	checkCoverage(t, "pytest-stray", []byte(in), res)
}

func TestReviewDetection(t *testing.T) { // M1
	data := fixtures(t)["gotest-json.txt"]
	in := append([]byte("go: downloading example.com/x v1.2.3\n"), data...)
	res, err := Reduce(bytes.NewReader(in), Options{})
	if err != nil || res.Format != "gotest-json" {
		t.Fatalf("format %v, %v", res.Format, err)
	}
	// go test text after more than 64 KiB of other lines.
	long := strings.Repeat("go: downloading example.com/some/module v1.2.3\n", 2000) +
		"=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    a_test.go:3: boom\nFAIL\n"
	res = reduceString(t, long, Options{Budget: 600})
	if res.Format != "gotest" || res.Records["fail"] != 1 {
		t.Errorf("format %s, fails %d", res.Format, res.Records["fail"])
	}
	// Test failures inside a log are errors.
	for _, l := range []string{"--- FAIL: TestA (0.00s)", "FAIL\texample.com/x\t0.1s", "FAILED tests/x.py::t - AssertionError"} {
		if levelOf(l) != KindError {
			t.Errorf("%q not an error", l)
		}
	}
}

func TestReviewPytestFailAndTeardownError(t *testing.T) { // M2
	in := "============================= test session starts ==============================\n" +
		"t.py::test_x FAILED [ 50%]\n" +
		"t.py::test_x ERROR [ 50%]\n" +
		"t.py::test_y PASSED [100%]\n" +
		"=================================== FAILURES ===================================\n" +
		"____ test_x ____\n" +
		"E   assert 1 == 2\n" +
		"==================================== ERRORS ====================================\n" +
		"____ ERROR at teardown of test_x ____\n" +
		"E   RuntimeError: cleanup\n" +
		"=========================== short test summary info ============================\n" +
		"FAILED t.py::test_x - assert 1 == 2\n" +
		"ERROR t.py::test_x - RuntimeError: cleanup\n" +
		"==================== 1 failed, 1 passed, 1 error in 0.01s =====================\n"
	res := reduceString(t, in+strings.Repeat("\n", 400), Options{})
	if res.Records["fail"] != 1 || res.Records["error"] != 1 {
		t.Errorf("fail %d error %d", res.Records["fail"], res.Records["error"])
	}
	checkCoverage(t, "pytest-fail-error", []byte(in+strings.Repeat("\n", 400)), res)
}

func TestReviewDetailCapped(t *testing.T) { // M3
	r := &genReader{n: 300000, f: func(i int) string {
		if i == 0 {
			return "ERROR something broke\n"
		}
		return "    at frame number " + strings.Repeat("x", i%7) + "\n"
	}}
	res, err := Reduce(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "more lines") {
		t.Errorf("long detail not noted:\n%s", res.Text)
	}
}

func TestReviewGoTestJSONEscapes(t *testing.T) { // M5
	in := `{"Action":"run","Package":"p","Test":"TestA"}` + "\n" +
		`{"Action":"output","Package":"p","Test":"TestA","Output":"\u001b]52;c;ZWNobyBoaQ==\u0007 \u001b[31mred\u001b[0m msg\n"}` + "\n" +
		`{"Action":"fail","Package":"p","Test":"TestA","Elapsed":0}` + "\n"
	res := reduceString(t, in+strings.Repeat(`{"Action":"output","Package":"p","Output":"x\n"}`+"\n", 200), Options{})
	if strings.ContainsAny(res.Text, "\x1b\x07") {
		t.Errorf("escape sequences reached the output: %q", res.Text)
	}
	if !strings.Contains(res.Text, "red msg") {
		t.Errorf("message lost:\n%s", res.Text)
	}
}

func TestReviewJUnitVariants(t *testing.T) { // M7, L4
	esc := `<?xml version="1.0"?><testsuite name="s" tests="2" failures="1">` +
		`<testcase classname="c" name="ok"/>` +
		"<testcase classname=\"c\" name=\"bad\"><failure message=\"boom\"><![CDATA[\x1b[31mExpected 1\x1b[0m]]></failure></testcase></testsuite>"
	res := reduceString(t, esc, Options{})
	if res.Format != "junit" || res.Records["fail"] != 1 || !strings.Contains(res.Text, "Expected 1") {
		t.Errorf("control characters: %s %v\n%s", res.Format, res.Records, res.Text)
	}
	cut := `<?xml version="1.0"?><testsuite name="s"><testcase classname="c" name="ok"/>` +
		`<testcase classname="c" name="slow"><system-out>still running`
	res = reduceString(t, cut, Options{})
	if res.Records["fail"] != 1 || !strings.Contains(res.Text, "cut off") {
		t.Errorf("cut report: %v\n%s", res.Records, res.Text)
	}
	multi := `<?xml version="1.0"?><testsuites>` +
		`<testsuite name="a" tests="2" failures="1"><testcase name="a1"/><testcase name="a2"><failure/></testcase></testsuite>` +
		`<testsuite name="b" tests="3" failures="2"><testcase name="b1"/><testcase name="b2"><failure/></testcase><testcase name="b3"><failure/></testcase></testsuite>` +
		`</testsuites>`
	res = reduceString(t, multi, Options{})
	if !strings.Contains(res.Text, "JUnit report: 5 tests, 3 failures") {
		t.Errorf("suite counts not added:\n%s", res.Text)
	}
}

func TestReviewKeepOnPasses(t *testing.T) { // L1
	short := fixtures(t)["pytest-short.txt"]
	res, err := Reduce(bytes.NewReader(short), Options{Keep: []*regexp.Regexp{regexp.MustCompile(`counted`)}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Text, "MATCHED") {
		t.Errorf("keep rule matched records with no lines:\n%s", res.Text)
	}
	in := `{"Action":"run","Package":"p","Test":"TestA"}` + "\n" +
		`{"Action":"output","Package":"p","Test":"TestA","Output":"warning: deprecated call\n"}` + "\n" +
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0}` + "\n"
	res = reduceString(t, in+strings.Repeat(`{"Action":"output","Package":"p","Output":"x\n"}`+"\n", 200),
		Options{Keep: []*regexp.Regexp{regexp.MustCompile(`deprecated`)}})
	if !strings.Contains(res.Text, "MATCHED") || !strings.Contains(res.Text, "TestA") {
		t.Errorf("keep rule missed a passing test's output:\n%s", res.Text)
	}
}

func TestReviewJSONAwkwardKeys(t *testing.T) { // L2
	var b strings.Builder
	b.WriteString(`{"my key \"q\"": "`)
	b.WriteString(strings.Repeat("long text ", 2000))
	b.WriteString(`", "x": 1}`)
	in := b.String()
	res := reduceString(t, in, Options{Budget: 1024})
	m := regexp.MustCompile(`ref ([0-9a-f]{12}):(/[^"\s)]+)`).FindStringSubmatch(res.Text)
	if m == nil {
		t.Fatalf("no pointer reference:\n%s", res.Text)
	}
	got, err := Expand([]byte(in), res.Index, m[2])
	if err != nil || !strings.HasPrefix(string(got), `"long text`) {
		t.Errorf("%s: %.40q %v", m[2], got, err)
	}
}

func TestReviewIndentedError(t *testing.T) { // L7
	in := "INFO starting worker\n    ERROR: config file missing\n" + strings.Repeat("INFO tick\n", 300)
	res := reduceString(t, in, Options{})
	if res.Records["error"] != 1 {
		t.Errorf("errors %d:\n%s", res.Records["error"], res.Text)
	}
}
