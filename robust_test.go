package abbreviatory

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reduceString(t *testing.T, s string, opt Options) *Result {
	t.Helper()
	res, err := Reduce(strings.NewReader(s), opt)
	if err != nil {
		t.Fatalf("Reduce: %v", err)
	}
	return res
}

func TestEmptyInput(t *testing.T) {
	res := reduceString(t, "", Options{})
	if res.InputLines != 0 || res.InputBytes != 0 {
		t.Errorf("got %d lines, %d bytes", res.InputLines, res.InputBytes)
	}
	if !strings.HasPrefix(res.Text, "[abbreviatory] log, 0 lines (0 B)") {
		t.Errorf("unexpected text %q", res.Text)
	}
}

func TestBinaryRefused(t *testing.T) {
	bin := make([]byte, 4096)
	rand.New(rand.NewSource(1)).Read(bin)
	if _, err := Reduce(bytes.NewReader(bin), Options{}); !errors.Is(err, ErrBinary) {
		t.Errorf("random bytes: got %v, want ErrBinary", err)
	}
	if _, err := Reduce(strings.NewReader("hello\x00world\n"), Options{}); !errors.Is(err, ErrBinary) {
		t.Errorf("NUL byte: got %v, want ErrBinary", err)
	}
}

func TestBudgetTooSmall(t *testing.T) {
	_, err := Reduce(strings.NewReader("ERROR x\n"), Options{Budget: 100})
	var be *BudgetError
	if !errors.As(err, &be) || be.Need != MinBudget {
		t.Fatalf("got %v", err)
	}
}

func TestUnknownFormatAndPreserve(t *testing.T) {
	if _, err := Reduce(strings.NewReader("x\n"), Options{Format: "yaml"}); err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Errorf("format: %v", err)
	}
	if _, err := Reduce(strings.NewReader("x\n"), Options{Preserve: []string{"bugs"}}); err == nil {
		t.Error("preserve: no error")
	}
}

func TestMalformedJSON(t *testing.T) {
	_, err := Reduce(strings.NewReader(`{"a": [1, 2,`), Options{Format: "json"})
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("got %v", err)
	}
	// Auto-detection of something that only looks like JSON falls back to
	// an error, not a crash.
	_, err = Reduce(strings.NewReader("{not json at all\nsecond line\n"), Options{})
	if err == nil {
		t.Error("no error for broken JSON")
	}
}

func TestTruncatedJUnit(t *testing.T) {
	x := `<?xml version="1.0"?><testsuite tests="3" failures="1"><testcase classname="a" name="ok1"/>` +
		`<testcase classname="a" name="bad"><failure message="boom">trace</failure></testcase>` +
		`<testcase classname="a" name="cut`
	res, err := Reduce(strings.NewReader(x), Options{})
	if err != nil {
		t.Fatalf("truncated report: %v", err)
	}
	if !strings.Contains(res.Text, "a.bad") || !strings.Contains(res.Text, "boom") {
		t.Errorf("failure missing:\n%s", res.Text)
	}
}

func TestLongLine(t *testing.T) {
	long := "ERROR " + strings.Repeat("x", 1<<20) + "\nINFO short\n"
	var sink bytes.Buffer
	res, err := Reduce(strings.NewReader(long), Options{Sink: &sink})
	if err != nil {
		t.Fatal(err)
	}
	if sink.String() != long {
		t.Error("sink lost bytes of the long line")
	}
	if res.InputLines != 2 || len(res.Text) > DefaultBudget {
		t.Errorf("lines %d, text %d bytes", res.InputLines, len(res.Text))
	}
	if !strings.Contains(res.Text, "chars)") {
		t.Errorf("long line not cut:\n%s", res.Text)
	}
	got, _ := Expand([]byte(long), res.Index, "L1")
	if string(got) != long[:len(long)-len("INFO short\n")] {
		t.Error("L1 doesn't expand to the whole long line")
	}
}

func TestCRLFAndANSI(t *testing.T) {
	in := "\x1b[31mERROR\x1b[0m disk full on /dev/sda1\r\n\x1b[33mWARN\x1b[0m low memory\r\nok\r\n"
	res := reduceString(t, in, Options{})
	if strings.Contains(res.Text, "\x1b") || strings.Contains(res.Text, "\r") {
		t.Errorf("escape codes or CR in output: %q", res.Text)
	}
	if !strings.Contains(res.Text, "ERROR disk full on /dev/sda1") || !strings.Contains(res.Text, "WARN low memory") {
		t.Errorf("levels not found:\n%s", res.Text)
	}
	got, _ := Expand([]byte(in), res.Index, "error")
	if string(got) != "\x1b[31mERROR\x1b[0m disk full on /dev/sda1\r\n" {
		t.Errorf("expand changed the bytes: %q", got)
	}
}

func TestInvalidUTF8(t *testing.T) {
	in := "ERROR bad byte \xff\xfe here\nfine\n"
	res := reduceString(t, in, Options{})
	if !strings.Contains(res.Text, "bad byte ?? here") {
		t.Errorf("invalid UTF-8 not replaced:\n%s", res.Text)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	in := "INFO a\nERROR b"
	res := reduceString(t, in, Options{})
	if res.InputLines != 2 {
		t.Errorf("lines = %d", res.InputLines)
	}
	got, _ := Expand([]byte(in), res.Index, "error")
	if string(got) != "ERROR b" {
		t.Errorf("got %q", got)
	}
}

func TestNoRefs(t *testing.T) {
	res := reduceString(t, "ERROR a\nWARN b\nINFO c\n", Options{NoRefs: true})
	if strings.Contains(res.Text, "[ref") || strings.Contains(res.Text, "ref ") {
		t.Errorf("references in -no-store output:\n%s", res.Text)
	}
}

func TestKeepRule(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&b, "INFO request %d ok\n", i)
	}
	b.WriteString("INFO tenant=acme quota reached\n")
	res := reduceString(t, b.String(), Options{Keep: []*regexp.Regexp{regexp.MustCompile(`quota`)}})
	if !strings.Contains(res.Text, "MATCHED --keep") || !strings.Contains(res.Text, "quota reached") {
		t.Errorf("keep rule ignored:\n%s", res.Text)
	}
}

func TestExpandSelectors(t *testing.T) {
	in := "INFO a\nERROR b\nWARN c\nERROR b\n"
	res := reduceString(t, in, Options{})
	for sel, want := range map[string]string{
		"":      in,
		"L2":    "ERROR b\n",
		"L2-3":  "ERROR b\nWARN c\n",
		"error": "ERROR b\nERROR b\n",
		"warn":  "WARN c\n",
		"info":  "INFO a\n",
	} {
		got, err := Expand([]byte(in), res.Index, sel)
		if err != nil || string(got) != want {
			t.Errorf("%q: got %q, %v; want %q", sel, got, err, want)
		}
	}
	for _, bad := range []string{"L0", "L3-2", "L9", "g99", "nonsense", "/x"} {
		if _, err := Expand([]byte(in), res.Index, bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestExpandJSON(t *testing.T) {
	in := `{"a": {"b~c": [10, {"error": "x"}]}, "d/e": true}`
	res := reduceString(t, in, Options{})
	for sel, want := range map[string]string{
		"/a/b~0c/0": "10\n",
		"/a/b~0c/1": `{"error": "x"}` + "\n",
		"/d~1e":     "true\n",
		"errors":    "/a/b~0c/1/error\t\"x\"\n",
	} {
		got, err := Expand([]byte(in), res.Index, sel)
		if err != nil || string(got) != want {
			t.Errorf("%q: got %q, %v; want %q", sel, got, err, want)
		}
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string][2]string{
		"3f2a9c1b7e04:g12":       {"3f2a9c1b7e04", "g12"},
		"[ref 3f2a9c1b7e04:L5]":  {"3f2a9c1b7e04", "L5"},
		"3f2a9c1b7e04":           {"3f2a9c1b7e04", ""},
		"3f2a9c1b7e04:/items/3":  {"3f2a9c1b7e04", "/items/3"},
		" ref 3f2a9c1b7e04:fail": {"3f2a9c1b7e04", "fail"},
	} {
		id, sel, err := ParseRef(in)
		if err != nil || id != want[0] || sel != want[1] {
			t.Errorf("%q: %q %q %v", in, id, sel, err)
		}
	}
	for _, bad := range []string{"", "xyz", "3F2A9C1B7E04:g1", "3f2a9c:g1"} {
		if _, _, err := ParseRef(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestNormalize(t *testing.T) {
	same := [][2]string{
		{"2026-10-18T08:00:00.074Z WARN slow 120ms", "2026-10-19T09:13:41.900Z WARN slow 9ms"},
		{"request_id=9c9095ed done", "request_id=bceefffb done"},
		{"uuid 550e8400-e29b-41d4-a716-446655440000", "uuid 123e4567-e89b-12d3-a456-426614174000"},
		{"addr 0xc000123456", "addr 0x7ff"},
		{"a  b\tc", "a b c"},
		{"user42 failed", "user7 failed"},
	}
	for _, p := range same {
		if normalize(p[0]) != normalize(p[1]) {
			t.Errorf("%q and %q normalize differently: %q vs %q", p[0], p[1], normalize(p[0]), normalize(p[1]))
		}
	}
	diff := [][2]string{
		{"connection refused", "connection reset"},
		{"WARN x", "ERROR x"},
		{"deadline exceeded", "deadline missed"},
	}
	for _, p := range diff {
		if normalize(p[0]) == normalize(p[1]) {
			t.Errorf("%q and %q normalize the same", p[0], p[1])
		}
	}
}

func TestLevels(t *testing.T) {
	cases := map[string]Kind{
		"2026-10-18 ERROR db down":                        KindError,
		"[ERROR] x":                                       KindError,
		`{"level":"error","msg":"x"}`:                     KindError,
		"time=1 level=warn msg=x":                         KindWarn,
		"E1018 12:00:00.000 1 main.go:3] failed":          KindError,
		"W1018 12:00:00.000 1 main.go:3] slow":            KindWarn,
		"src/main.c:3:1: error: expected ';'":             KindError,
		"warning: unused variable":                        KindWarn,
		"panic: boom":                                     KindError,
		"Exception in thread \"main\" java.lang.Error: x": KindError,
		"INFO 0 errors, 0 warnings":                       KindInfo,
		"ERRORS_TOTAL=0 is a metric name":                 KindInfo,
		"processed 5 items without error":                 KindInfo,
		"2026-10-18 WARNING disk 91%":                     KindWarn,
	}
	for in, want := range cases {
		if got := levelOf(in); got != want {
			t.Errorf("levelOf(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStackTraces(t *testing.T) {
	in := strings.Join([]string{
		"INFO start",
		"Traceback (most recent call last):",
		`  File "a.py", line 1, in <module>`,
		"    f()",
		"KeyError: 'x'",
		"INFO after",
		"panic: boom",
		"",
		"goroutine 1 [running]:",
		"main.main()",
		"\t/src/main.go:5 +0x1d",
		"exit status 2",
		"INFO end",
	}, "\n") + "\n" + strings.Repeat("INFO filler\n", 300)
	res := reduceString(t, in, Options{})
	if !strings.Contains(res.Text, "- KeyError: 'x'") {
		t.Errorf("traceback not headed by its exception:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "- panic: boom") || !strings.Contains(res.Text, "main.main()") {
		t.Errorf("panic not kept whole:\n%s", res.Text)
	}
	got, _ := Expand([]byte(in), res.Index, "error")
	if strings.Count(string(got), "\n") != 10 {
		t.Errorf("error lines: %q", got)
	}
}

func TestDedupe(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&b, "2026-10-18T08:%02d:%02dZ ERROR db: connection to 10.0.0.%d:5432 refused\n", i/60%60, i%60, i%9)
	}
	res := reduceString(t, b.String(), Options{})
	if !strings.Contains(res.Text, "x1000 2026-10-18T08:00:00Z ERROR db") {
		t.Errorf("repeats not grouped:\n%s", res.Text)
	}
}

func TestManyDistinctBounded(t *testing.T) {
	// More distinct messages than maxDistinct: they share a catch-all group
	// and every line stays referenced.
	letters := func(i int) string {
		// Digits spelled as letters g..p, so normalizing keeps them apart.
		s := []byte(strconv.Itoa(i))
		for k := range s {
			s[k] = 'g' + (s[k] - '0')
		}
		return string(s)
	}
	r := &genReader{n: maxDistinct + 5000, f: func(i int) string { return "INFO event " + letters(i) + "\n" }}
	res, err := Reduce(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Index.Groups) > maxDistinct+1 {
		t.Errorf("%d groups", len(res.Index.Groups))
	}
	var n int64
	for _, g := range res.Index.Groups {
		for _, rg := range g.Ranges {
			n += rg[1] - rg[0] + 1
		}
	}
	if n != res.InputLines {
		t.Errorf("groups cover %d of %d lines", n, res.InputLines)
	}
}

// genReader produces n lines from f without holding them in memory.
type genReader struct {
	n, i int
	f    func(int) string
	buf  []byte
}

func (g *genReader) Read(p []byte) (int, error) {
	for len(g.buf) < len(p) && g.i < g.n {
		g.buf = append(g.buf, g.f(g.i)...)
		g.i++
	}
	if len(g.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, g.buf)
	g.buf = g.buf[n:]
	return n, nil
}

// TestRandomLogs runs the promise checks on seeded random logs.
func TestRandomLogs(t *testing.T) {
	words := []string{"db", "cache", "queue", "auth", "disk", "net", "worker"}
	levels := []string{"INFO", "INFO", "INFO", "DEBUG", "WARN", "ERROR"}
	for seed := int64(1); seed <= 25; seed++ {
		rnd := rand.New(rand.NewSource(seed))
		var b strings.Builder
		n := rnd.Intn(3000)
		for i := 0; i < n; i++ {
			lv := levels[rnd.Intn(len(levels))]
			fmt.Fprintf(&b, "t=%d %s %s %s id=%d\n", i, lv, words[rnd.Intn(len(words))], words[rnd.Intn(len(words))], rnd.Intn(50))
			if lv == "ERROR" && rnd.Intn(4) == 0 {
				for k := rnd.Intn(5); k >= 0; k-- {
					fmt.Fprintf(&b, "\tat frame%d\n", rnd.Intn(3))
				}
			}
		}
		data := []byte(b.String())
		for _, budget := range []int{512, 900, 2000, 4096} {
			checkPromise(t, "random-"+strconv.FormatInt(seed, 10), data, Options{Budget: budget})
		}
	}
}

// TestLargeInputMemory streams a large generated log through Reduce and
// checks that the heap stays far below the input size. The size is set by
// ABBREVIATORY_BIG_MB (default 64 MB; scripts/record-tests.sh uses 1024).
func TestLargeInputMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	mb := 64
	if v := os.Getenv("ABBREVIATORY_BIG_MB"); v != "" {
		mb, _ = strconv.Atoi(v)
	}
	line := func(i int) string {
		switch {
		case i%997 == 0:
			return "2026-10-18T08:00:00Z ERROR db: connection to 10.0.0." + strconv.Itoa(i%9) + ":5432 refused id=" + strconv.Itoa(i) + "\n"
		case i%101 == 0:
			return "2026-10-18T08:00:00Z WARN slow query took " + strconv.Itoa(i%4000) + "ms\n"
		default:
			return "2026-10-18T08:00:00Z INFO http method=GET path=/api/orders/" + strconv.Itoa(i) + " status=200 duration_ms=" + strconv.Itoa(i%180) + "\n"
		}
	}
	avg := len(line(1))
	n := mb << 20 / avg
	var peak uint64
	done := make(chan struct{})
	go func() {
		var ms runtime.MemStats
		for {
			select {
			case <-done:
				return
			default:
			}
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak {
				peak = ms.HeapInuse
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	start := time.Now()
	res, err := Reduce(&genReader{n: n, f: line}, Options{})
	took := time.Since(start)
	close(done)
	if err != nil {
		t.Fatal(err)
	}
	if res.InputBytes < int64(mb)<<20*9/10 {
		t.Fatalf("generated only %d bytes", res.InputBytes)
	}
	t.Logf("memory: %d MB input, %d lines, peak heap in use %.1f MB, output %d bytes, %.1f s (%.0f MB/s)",
		res.InputBytes>>20, res.InputLines, float64(peak)/(1<<20), len(res.Text), took.Seconds(), float64(res.InputBytes)/(1<<20)/took.Seconds())
	if peak > 64<<20 {
		t.Errorf("peak heap %d MB for a %d MB input", peak>>20, mb)
	}
}
