package abbreviatory

import (
	"regexp"
	"strconv"
	"strings"
)

// Rust: rustc's diagnostics as cargo build, check, clippy and test print
// them, and cargo test's results.
//
// A diagnostic is a level line at the start of a line ("error[E0308]:
// mismatched types", "warning: unused variable: `y`") followed by its
// location ("  --> src/parse.rs:12:26"), the code snippet and notes, up to
// the next blank line. Its head names the location, so every error is
// addressable by file and line.

var (
	// rustDiag is the first line of a diagnostic.
	rustDiag = regexp.MustCompile(`^(error|warning)(\[[A-Za-z0-9_:-]+\])?: `)
	// rustGenerated is cargo's count of a crate's warnings: a summary, not
	// a warning of its own.
	rustGenerated = regexp.MustCompile("^warning: `[^`]+` \\([^)]*\\) generated [0-9]+ warnings?")
	// cargoStatus is one of cargo's right-aligned status lines.
	cargoStatus  = regexp.MustCompile(`^ {0,12}(Compiling|Checking|Finished|Running|Fresh|Downloaded|Downloading|Updating|Locking|Adding|Removing|Blocking|Documenting|Doc-tests|Packaging|Verifying|Installing|Installed|Replacing|Building|Waiting|Unpacking|Updated|Scraping|Executable|Built|Summary|Archiving|Uploading|Dirty|Skipping|Ignored|Vendoring|Migrating|Packaged)\b`)
	rustLocation = regexp.MustCompile(`^\s*--> (\S.*)$`)
	// cargoTestLine is one test's result.
	cargoTestLine = regexp.MustCompile(`^test (.+?) \.\.\. (ok|FAILED|ignored(?:, .*)?|bench:.*)$`)
	cargoTestFail = regexp.MustCompile(`^---- (.+?) (stdout|stderr) ----$`)
	cargoResult   = regexp.MustCompile(`^test result: `)
)

// rustDiags reads diagnostics; other lines go back to the caller.
type rustDiags struct {
	b block
}

func (d *rustDiags) line(n int64, text string, emit func(Record)) bool {
	if m := rustDiag.FindStringSubmatch(text); m != nil && !rustGenerated.MatchString(text) {
		k := KindWarn
		if m[1] == "error" {
			k = KindError
		}
		d.b.start(n, k, text, emit)
		return true
	}
	if !d.b.open() {
		return false
	}
	if strings.TrimSpace(text) == "" || cargoStatus.MatchString(text) || rustGenerated.MatchString(text) ||
		strings.HasPrefix(text, "Some errors have detailed explanations") || strings.HasPrefix(text, "For more information about") ||
		cargoTestLine.MatchString(text) || strings.HasPrefix(text, "running ") {
		d.flush(emit)
		return false
	}
	if m := rustLocation.FindStringSubmatch(text); m != nil && !strings.Contains(d.b.pend.Head, " at ") && len(d.b.pend.Detail) == 0 {
		d.b.pend.Head += " at " + m[1]
	}
	d.b.add(n, text)
	return true
}

func (d *rustDiags) flush(emit func(Record)) { d.b.flush(emit) }

// cargo reads cargo build, check and clippy output.
type cargo struct {
	diags   rustDiags
	summary []string
}

func newCargo() *cargo { return &cargo{} }

func (c *cargo) line(n int64, text string, emit func(Record)) {
	if c.diags.line(n, text, emit) {
		return
	}
	if rustGenerated.MatchString(text) {
		c.summary = append(c.summary, text)
		infoLine(n, text, emit)
		return
	}
	if cargoStatus.MatchString(text) || strings.HasPrefix(text, "Some errors have detailed explanations") || strings.HasPrefix(text, "For more information about") {
		infoLine(n, text, emit)
		return
	}
	plainLine(n, text, emit)
}

func (c *cargo) finish(emit func(Record)) []string {
	c.diags.flush(emit)
	return c.summary
}

// looksCargo reports rustc diagnostics: a level line followed by a
// location line, or cargo's status lines.
func looksCargo(sample []byte) bool {
	lines := strings.SplitN(string(sample), "\n", 400)
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if rustDiag.MatchString(l) && i+1 < len(lines) && rustLocation.MatchString(strings.TrimRight(lines[i+1], "\r")) {
			return true
		}
		if strings.HasPrefix(l, "   Compiling ") || strings.HasPrefix(l, "    Checking ") ||
			(strings.HasPrefix(l, "    Finished ") && strings.Contains(l, "target(s) in")) {
			return true
		}
	}
	return false
}

// looksCargoTest reports cargo test's results.
func looksCargoTest(sample []byte) bool {
	s := string(sample)
	if !strings.Contains(s, "\nrunning ") && !strings.HasPrefix(s, "running ") {
		return false
	}
	for _, l := range strings.SplitN(s, "\n", 4000) {
		l = strings.TrimRight(l, "\r")
		if cargoTestLine.MatchString(l) || cargoResult.MatchString(l) {
			return true
		}
	}
	return false
}

// cargoTest reads cargo test: every test's line, the failures section with
// each failing test's output, and the counts. Compiler diagnostics before
// the tests are read as in cargo build.
type cargoTest struct {
	diags   rustDiags
	target  string // the test binary being run ("unittests src/lib.rs")
	tests   []*cargoCase
	byName  map[string]*cargoCase
	out     *cargoCase // the failure section being read
	outLine int64
	summary []string
	listing bool // in the plain "failures:" list of names
}

type cargoCase struct {
	target, name, status string
	lines                []Range
	detail               []string
	more                 int64
}

func newCargoTest() *cargoTest { return &cargoTest{byName: map[string]*cargoCase{}} }

func (c *cargoTest) key(name string) string { return c.target + "\x00" + name }

func (c *cargoTest) line(n int64, text string, emit func(Record)) {
	if m := cargoTestFail.FindStringSubmatch(text); m != nil {
		c.diags.flush(emit)
		c.listing = false
		if tc := c.byName[c.key(m[1])]; tc != nil {
			c.out = tc
			tc.lines = addRange(tc.lines, Range{n, n})
			return
		}
		c.out = nil
	}
	if c.out != nil {
		// A failure section runs to the next section, the list of names or
		// the result line.
		if text == "failures:" || cargoResult.MatchString(text) || strings.HasPrefix(text, "test ") && cargoTestLine.MatchString(text) {
			c.out = nil
		} else {
			tc := c.out
			if t := strings.TrimSpace(text); t != "" {
				if len(tc.detail) < maxDetail {
					tc.detail = append(tc.detail, strings.TrimRight(text, " \t"))
				} else {
					tc.more++
				}
			}
			tc.lines = addRange(tc.lines, Range{n, n})
			return
		}
	}
	if c.diags.line(n, text, emit) {
		return
	}
	switch {
	case strings.HasPrefix(text, "     Running ") || strings.HasPrefix(text, "   Doc-tests "):
		c.target = cargoTarget(strings.TrimSpace(text))
		c.listing = false
		infoLine(n, text, emit)
	case cargoTestLine.MatchString(text):
		m := cargoTestLine.FindStringSubmatch(text)
		tc := &cargoCase{target: c.target, name: m[1], status: m[2], lines: []Range{{n, n}}}
		if tc.status != "FAILED" {
			// Passes and skips need nothing that comes later: emitted now,
			// so a run of a million tests holds only its failures.
			emit(tc.record())
			return
		}
		for i := 2; c.byName[c.key(tc.name)] != nil; i++ {
			// The same name failing twice in one binary: keep both.
			tc.name = m[1] + " (again " + strconv.Itoa(i) + ")"
		}
		c.byName[c.key(tc.name)] = tc
		c.tests = append(c.tests, tc)
	case text == "failures:":
		c.listing = true
		infoLine(n, text, emit)
	case c.listing && strings.HasPrefix(text, "    ") && c.byName[c.key(strings.TrimSpace(text))] != nil:
		tc := c.byName[c.key(strings.TrimSpace(text))]
		tc.lines = addRange(tc.lines, Range{n, n})
	case cargoResult.MatchString(text):
		c.listing = false
		c.summary = append(c.summary, text)
		infoLine(n, text, emit)
	case strings.HasPrefix(text, "error: test failed") || strings.HasPrefix(text, "error: doctest failed"):
		// cargo's last word: which target to rerun.
		emit(Record{Kind: KindError, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
	case rustGenerated.MatchString(text) || cargoStatus.MatchString(text) || strings.HasPrefix(text, "running "):
		infoLine(n, text, emit)
	default:
		plainLine(n, text, emit)
	}
}

func (c *cargoTest) finish(emit func(Record)) []string {
	c.diags.flush(emit)
	for _, tc := range c.tests {
		emit(tc.record())
	}
	return c.summary
}

func (tc *cargoCase) record() Record {
	id := tc.name
	if tc.target != "" {
		id = tc.target + " " + tc.name
	}
	r := Record{ID: id, Name: tc.name, Head: tc.name, Lines: mergeRanges(tc.lines)}
	switch {
	case tc.status == "ok" || strings.HasPrefix(tc.status, "bench:"):
		r.Kind = KindPass
	case tc.status == "FAILED":
		r.Kind = KindFail
		r.Detail = tc.detail
		if tc.more > 0 {
			r.Detail = append(r.Detail, "... ("+plural(tc.more, "more line", "more lines")+")")
		}
		// The panic message says why: show it in the head.
		if msg := cargoPanicMessage(tc.detail); msg != "" {
			r.Head = tc.name + " - " + msg
		}
	default:
		r.Kind = KindSkip
	}
	return r
}

// cargoTarget names the test binary of a "Running" or "Doc-tests" line.
// Every crate's library tests are "unittests src/lib.rs", so in a workspace
// the binary's name (its file name without the hash) tells them apart:
// "Running unittests src/lib.rs (target/debug/deps/core-1a2b3c)" is
// "core unittests src/lib.rs".
func cargoTarget(line string) string {
	desc, bin, ok := strings.Cut(line, " (")
	if !ok {
		return line
	}
	bin = strings.TrimSuffix(bin, ")")
	if i := strings.LastIndexAny(bin, "/\\"); i >= 0 {
		bin = bin[i+1:]
	}
	bin = strings.TrimSuffix(bin, ".exe")
	if i := strings.LastIndexByte(bin, '-'); i > 0 && allHex(bin[i+1:]) {
		bin = bin[:i]
	}
	desc = strings.TrimPrefix(desc, "Running ")
	if bin == "" {
		return desc
	}
	return bin + " " + desc
}

// cargoPanicMessage is the line after "thread '...' panicked at ...:", or,
// before Rust 1.73, the quoted message on that line itself ("panicked at
// 'boom', src/lib.rs:4:5").
func cargoPanicMessage(detail []string) string {
	for i, l := range detail {
		if strings.HasPrefix(l, "thread '") && strings.Contains(l, "panicked at") {
			if _, rest, ok := strings.Cut(l, "panicked at '"); ok {
				if j := strings.LastIndex(rest, "', "); j >= 0 {
					return rest[:j]
				}
			}
			if i+1 < len(detail) {
				return strings.TrimSpace(detail[i+1])
			}
		}
	}
	return ""
}
