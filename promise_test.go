package abbreviatory

// These tests hold Abbreviatory to its promise, on every fixture and at
// every budget:
//
//   - the output never exceeds the budget (or the call fails saying so);
//   - every preserved item (failure, error, warning, keep-rule match) is
//     either shown with its own reference or counted in a "not shown" line
//     whose reference expands to it;
//   - the same input and options give byte-identical output;
//   - every reference expands back, byte for byte, to the original lines it
//     stands for, and together the groups account for every input line.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var budgets = []int{256, 512, 700, 1024, 2048, 4096, 16384}

var preserveSets = [][]string{
	nil,
	{"failures"},
	{"errors", "warnings"},
	{},
}

func fixtures(t *testing.T) map[string][]byte {
	t.Helper()
	paths, err := filepath.Glob("test/fixtures/*")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, p := range paths {
		if fi, err := os.Stat(p); err != nil || fi.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(p)] = data
	}
	if len(out) < 8 {
		t.Fatalf("expected the fixtures in test/fixtures, found %d", len(out))
	}
	return out
}

// wantFormat is the format each fixture must be detected as.
var wantFormat = map[string]string{
	"gotest-json.txt":    "gotest-json",
	"gotest-verbose.txt": "gotest",
	"gotest-short.txt":   "gotest",
	"pytest-verbose.txt": "pytest",
	"pytest-short.txt":   "pytest",
	"junit-pytest.xml":   "junit",
	"app.log":            "logs",
	"api-response.json":  "json",
}

func TestDetection(t *testing.T) {
	for name, data := range fixtures(t) {
		want, ok := wantFormat[name]
		if !ok {
			t.Errorf("fixture %s has no expected format in wantFormat", name)
			continue
		}
		res, err := Reduce(bytes.NewReader(data), Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Format != want {
			t.Errorf("%s detected as %s, want %s", name, res.Format, want)
		}
	}
}

// refRE finds references as the text shows them: "[ref ID:sel]" in reports,
// and in JSON outlines "ref": "ID:/pointer" or "(+N chars, ref ID:/pointer)".
var refRE = regexp.MustCompile(`(?:\[ref |"ref": "|, ref )([0-9a-f]{12})(?::([^\]"\s)]+))?`)

func TestPromise(t *testing.T) {
	keep := []*regexp.Regexp{regexp.MustCompile(`deprecated|TestFieldHex|Rounding`)}
	for name, data := range fixtures(t) {
		for _, b := range budgets {
			t.Run(name+"/"+strconv.Itoa(b), func(t *testing.T) {
				t.Parallel()
				for _, ps := range preserveSets {
					for _, k := range [][]*regexp.Regexp{nil, keep} {
						checkPromise(t, name, data, Options{Budget: b, Preserve: ps, Keep: k})
					}
				}
			})
		}
	}
}

func checkPromise(t *testing.T, name string, data []byte, opt Options) {
	t.Helper()
	label := name + " budget=" + strconv.Itoa(opt.Budget) + " preserve=" + strings.Join(opt.Preserve, ",")
	if opt.Preserve == nil {
		label += "(default)"
	}
	if len(opt.Keep) > 0 {
		label += " keep"
	}
	var sink bytes.Buffer
	opt.Sink = &sink
	res, err := Reduce(bytes.NewReader(data), opt)
	var be *BudgetError
	if errors.As(err, &be) {
		// A budget too small to account for everything is refused, never
		// broken.
		if be.Need <= opt.Budget {
			t.Errorf("%s: budget error with need %d <= budget %d", label, be.Need, opt.Budget)
		}
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if !bytes.Equal(sink.Bytes(), data) {
		t.Fatalf("%s: the sink got %d bytes, the input has %d", label, sink.Len(), len(data))
	}
	if res.InputBytes != int64(len(data)) {
		t.Errorf("%s: InputBytes %d, want %d", label, res.InputBytes, len(data))
	}

	// 1. The budget is hard.
	if len(res.Text) > opt.Budget {
		t.Errorf("%s: output is %d bytes, over the budget", label, len(res.Text))
	}

	// 2. Deterministic.
	opt2 := opt
	opt2.Sink = nil
	res2, err := Reduce(bytes.NewReader(data), opt2)
	if err != nil {
		t.Fatalf("%s: second run: %v", label, err)
	}
	if res2.Text != res.Text || res2.ID != res.ID {
		t.Errorf("%s: two runs gave different output", label)
	}

	// 3. Every reference resolves, and resolves to the right bytes.
	shownGroups := map[int]bool{}
	selectors := map[string]bool{}
	for _, m := range refRE.FindAllStringSubmatch(res.Text, -1) {
		if m[1] != res.ID {
			t.Errorf("%s: reference to %s, want %s", label, m[1], res.ID)
		}
		sel := m[2]
		selectors[sel] = true
		out, err := Expand(data, res.Index, sel)
		if err != nil {
			t.Errorf("%s: expanding %q: %v", label, sel, err)
			continue
		}
		if strings.HasPrefix(sel, "/") && !json.Valid(out) {
			t.Errorf("%s: %s expands to invalid JSON %.80q", label, sel, out)
		}
		if strings.HasPrefix(sel, "g") {
			i, _ := strconv.Atoi(sel[1:])
			shownGroups[i] = true
			if len(out) == 0 {
				t.Errorf("%s: %s expands to nothing", label, sel)
			}
		}
	}

	// 4. Preserved items are shown or referenced. A small input shown
	// whole shows everything.
	if res.Whole {
		for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
			if !strings.Contains(res.Text, clean([]byte(l))) {
				t.Errorf("%s: shown whole but lacks %q", label, l)
			}
		}
	} else if res.Family != FamilyJSON {
		rules, _ := preserveRules(opt.Preserve)
		overflow := false
		for i, g := range res.Index.Groups {
			k, _ := ParseKind(g.Kind)
			if !rules.has(k) && !g.Kept {
				continue
			}
			if shownGroups[i] {
				continue
			}
			overflow = true
			sel := g.Kind
			if g.Kept && !(rules.has(k) && (k == KindFail || k == KindError)) {
				sel = "keep"
			}
			if !selectors[sel] {
				t.Errorf("%s: preserved group g%d (%s) is neither shown nor referenced through %q\n%s", label, i, g.Kind, sel, res.Text)
				continue
			}
			// The kind reference must cover this group's lines.
			var kindRanges []Range
			for _, h := range res.Index.Groups {
				if (sel == "keep" && h.Kept) || h.Kind == sel {
					kindRanges = append(kindRanges, groupRanges(h)...)
				}
			}
			kindRanges = mergeRanges(kindRanges)
			for _, r := range groupRanges(g) {
				if !covered(kindRanges, r) {
					t.Errorf("%s: %s doesn't cover g%d's lines %d-%d", label, sel, i, r.Start, r.End)
				}
			}
		}
		if overflow != res.Overflow {
			t.Errorf("%s: Overflow is %v, but preserved items not shown: %v", label, res.Overflow, overflow)
		}
	}

	// 5. The whole input expands back exactly.
	if whole, err := Expand(data, res.Index, ""); err != nil || !bytes.Equal(whole, data) {
		t.Errorf("%s: the whole-input reference doesn't give the input back", label)
	}

	// 6. JSON output is valid JSON between the header and the footer.
	if res.Family == FamilyJSON && !strings.Contains(res.Text, "too large to outline") {
		lines := strings.Split(strings.TrimRight(res.Text, "\n"), "\n")
		body := lines[2:]
		if n := len(body); n > 0 && strings.HasPrefix(body[n-1], "not shown:") {
			body = body[:n-1]
		}
		if !json.Valid([]byte(strings.Join(body, "\n"))) {
			t.Errorf("%s: JSON body is not valid JSON:\n%s", label, res.Text)
		}
	}
}

// TestGroupsCoverInput checks that for line formats every input line belongs
// to exactly one group, so the counts and references account for all of
// it, and that each group expands to exactly its lines.
func TestGroupsCoverInput(t *testing.T) {
	for name, data := range fixtures(t) {
		res, err := Reduce(bytes.NewReader(data), Options{})
		if err != nil {
			t.Fatal(err)
		}
		ix := res.Index
		switch ix.Unit {
		case "lines":
			if res.Family == FamilyJSON {
				continue
			}
			lines := splitKeep(data)
			if int64(len(lines)) != res.InputLines || ix.Lines != res.InputLines {
				t.Fatalf("%s: %d lines, result says %d", name, len(lines), res.InputLines)
			}
			owner := make([]int, len(lines)+1)
			for gi, g := range ix.Groups {
				var want []byte
				for _, r := range g.Ranges {
					for n := r[0]; n <= r[1]; n++ {
						if n < 1 || n > int64(len(lines)) {
							t.Fatalf("%s: g%d has line %d outside the input", name, gi, n)
						}
						if owner[n] != 0 {
							t.Errorf("%s: line %d is in g%d and g%d", name, n, owner[n]-1, gi)
						}
						owner[n] = gi + 1
						want = append(want, lines[n-1]...)
					}
				}
				got, err := Expand(data, ix, "g"+strconv.Itoa(gi))
				if err != nil || !bytes.Equal(got, want) {
					t.Errorf("%s: g%d doesn't expand to its own lines", name, gi)
				}
			}
			for n := 1; n <= len(lines); n++ {
				if owner[n] == 0 {
					t.Errorf("%s: line %d (%q) is in no group", name, n, lines[n-1])
				}
			}
		case "bytes":
			// JUnit: each range is one whole testcase element.
			for gi, g := range ix.Groups {
				for _, r := range g.Ranges {
					part := data[r[0] : r[1]+1]
					if !bytes.HasPrefix(part, []byte("<testcase")) || !bytes.HasSuffix(part, []byte(">")) {
						t.Errorf("%s: g%d has a range that is not a testcase element: %.60q", name, gi, part)
					}
				}
			}
		}
	}
}

func covered(rs []Range, r Range) bool {
	for _, x := range rs {
		if x.Start <= r.Start && r.End <= x.End {
			return true
		}
	}
	return false
}

func splitKeep(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			out = append(out, data)
			break
		}
		out = append(out, data[:i+1])
		data = data[i+1:]
	}
	return out
}

// TestExpectedContent spot-checks that each fixture's reduction says what a
// person would want it to say.
func TestExpectedContent(t *testing.T) {
	cases := map[string][]string{
		"gotest-json.txt": {"tests: 1,204 passed, 7 failed, 1 skipped", "TestFieldSpaces", "TestFieldHex",
			"TestPickPast", "index out of range", `parsing " 12": invalid syntax`},
		"gotest-verbose.txt": {"tests: 1,204 passed, 7 failed, 1 skipped", "TestFieldUnits/size=4KB", "panic: runtime error"},
		"gotest-short.txt":   {"shown whole", "--- FAIL: TestFieldHex", `parsing "0xff"`},
		"pytest-verbose.txt": {"tests: 903 passed, 5 failed, 1 skipped; 1 error", "test_parse_price_comma",
			"could not convert string to float", "test_needs_db", "DeprecationWarning"},
		"pytest-short.txt": {"tests: 903 passed, 5 failed, 1 skipped; 1 error", "TestRounding::test_half_up"},
		"junit-pytest.xml": {"tests: 903 passed, 5 failed, 1 skipped; 1 error", "test_half_up", "AssertionError"},
		"app.log": {"x486", "connection to 10.0.4.", "IllegalStateException", "FileNotFoundError",
			"nil pointer dereference", "x1952", `config key "timeout" is deprecated`},
		"api-response.json": {"array of 2,500 objects", "PRICE_STALE", `"index": 311`, `"index": 2210`, "3 error fields"},
	}
	data := fixtures(t)
	for name, wants := range cases {
		res, err := Reduce(bytes.NewReader(data[name]), Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(res.Text, w) {
				t.Errorf("%s: output lacks %q:\n%s", name, w, res.Text)
			}
		}
	}
}

// TestRatios records the reduction on each fixture at the default budget;
// scripts/record-tests.sh writes these numbers to test/results.
func TestRatios(t *testing.T) {
	data := fixtures(t)
	names := make([]string, 0, len(data))
	for n := range data {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		res, err := Reduce(bytes.NewReader(data[n]), Options{})
		if err != nil {
			t.Fatal(err)
		}
		in, out := len(data[n]), len(res.Text)
		t.Logf("ratio %-20s %-12s %9d bytes %6d lines -> %5d bytes %3d lines (%.2f%% of the input)",
			n, res.Format, in, res.InputLines, out, strings.Count(res.Text, "\n"), 100*float64(out)/float64(in))
	}
}
