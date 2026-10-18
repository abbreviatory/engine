// Package abbreviatory reduces large tool output (test runs, logs, JSON) to a
// short report that keeps what matters by explicit rules: failures, errors,
// warnings and anything a keep rule matches. Repeats are grouped, everything
// left out stays addressable through references, and the result is
// deterministic: the same input and options give the same bytes.
//
// It uses no language model and makes no network calls.
package abbreviatory

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version is the release version. Raising it on main releases it.
const Version = "0.2.0"

// DefaultBudget is the output size in bytes when none is given.
const DefaultBudget = 4096

// MinBudget is the smallest budget accepted. Below it there is no room for
// the header, the counts and the references that account for what is left
// out.
const MinBudget = 512

// maxWhole is the largest JUnit report read whole, and maxJSON the largest
// JSON document (a parsed document takes many times its size in memory).
// Past them an auto-detected input is read as a log, which streams; with
// -format given, it is an error.
const (
	maxWhole = 256 << 20
	maxJSON  = 16 << 20
)

// Options control a reduction.
type Options struct {
	// Format is "auto" (or empty), a family ("test-output", "logs",
	// "json") or a format name (see FormatNames).
	Format string
	// Budget is the most bytes the reduced text may have. Zero means
	// DefaultBudget.
	Budget int
	// Preserve names what must be shown or referenced: any of "errors",
	// "failures", "warnings". Nil means all three.
	Preserve []string
	// Keep holds extra rules: lines matching any of them are preserved too.
	Keep []*regexp.Regexp
	// NoRefs leaves references out of the text, for when the original is
	// not stored.
	NoRefs bool
	// Sink, if set, receives the input bytes exactly as read, so the caller
	// can store the original for expansion.
	Sink io.Writer

	// Track compares this run with History, the earlier runs of the same
	// command (oldest first; empty for the first run). The report then says
	// what is new, fixed, still failing and flaky, and Result.Run is the
	// record to append to the history.
	Track   bool
	History []Run
	// Full keeps the whole report when nothing changed since the last run;
	// otherwise that report is one line.
	Full bool
	// Command and Exit describe the command that printed the input, when
	// known; they are kept in the run record and Exit is shown.
	Command string
	Exit    *int
}

// Result is a finished reduction.
type Result struct {
	Format  string // format name, such as "pytest"
	Family  string // FamilyTests, FamilyLogs or FamilyJSON
	Display string // format as shown in the header
	// ID names the stored original in references. It is a hash of the
	// format and the input, so the same input gives the same ID.
	ID         string
	InputBytes int64
	InputLines int64
	Text       string
	// Overflow is true when some preserved item didn't fit and is only
	// referenced, not shown.
	Overflow bool
	// Whole is true when the input was small enough to show whole: the
	// reduction would not have been shorter.
	Whole bool
	// Records counts records by kind; Lines counts the input lines they
	// cover.
	Records map[string]int64
	Lines   map[string]int64
	// Index maps references to original lines. Store it with the original
	// so references can be expanded.
	Index *Index
	// With Options.Track: the record of this run, to append to the
	// history, and what changed since the previous run.
	Run   *Run
	Delta *Delta
}

// Index records which original lines each group came from.
type Index struct {
	ID     string `json:"id"`
	Format string `json:"format"`
	Lines  int64  `json:"lines"`
	// Unit is "lines" when group ranges are 1-based line numbers, or
	// "bytes" when they are 0-based byte offsets (JUnit XML, which is often
	// one long line).
	Unit   string       `json:"unit"`
	Groups []IndexGroup `json:"groups"`
}

// IndexGroup is one group: its kind, whether a keep rule matched it, and its
// line ranges.
type IndexGroup struct {
	Kind   string     `json:"kind"`
	Kept   bool       `json:"kept,omitempty"`
	Ranges [][2]int64 `json:"ranges"`
}

// ErrBinary is returned for input that isn't text.
var ErrBinary = errors.New("input looks like binary data, not text")

// BudgetError is returned when the budget is too small.
type BudgetError struct{ Budget, Need int }

func (e *BudgetError) Error() string {
	return "budget of " + strconv.Itoa(e.Budget) + " bytes is too small; this input needs at least " + strconv.Itoa(e.Need)
}

// Reduce reads input and returns its reduction.
func Reduce(r io.Reader, opt Options) (*Result, error) {
	budget := opt.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	if budget < MinBudget {
		return nil, &BudgetError{Budget: budget, Need: MinBudget}
	}
	rules, err := preserveRules(opt.Preserve)
	if err != nil {
		return nil, err
	}

	br := bufio.NewReaderSize(r, 1<<20)
	sample, _ := br.Peek(1 << 20)
	if isBinary(sample[:min(len(sample), 64<<10)]) {
		return nil, ErrBinary
	}
	fi, err := detect(sample, opt.Format)
	if err != nil {
		return nil, err
	}
	auto := opt.Format == "" || opt.Format == "auto"

	h := sha256.New()
	var count countWriter
	writers := []io.Writer{h, &count}
	if opt.Sink != nil {
		writers = append(writers, opt.Sink)
	}
	in := io.TeeReader(br, io.MultiWriter(writers...))

	g := newGrouper(rules, opt.Keep)
	if opt.Track {
		g.passIDs = map[string]bool{}
	}
	var summary []string
	var doc *jsonDoc
	var small []string
	var inputLines int64
	var lineSrc io.Reader = in

	if fi.whole {
		limit := maxWhole
		if fi.name == "json" {
			limit = maxJSON
		}
		data, rerr := readWhole(in, limit)
		var perr error
		switch {
		case rerr != nil:
			perr = rerr
		case fi.name == "json":
			doc, perr = parseJSONDoc(data)
		default:
			g.bytes = true
			summary, perr = parseJUnit(data, g.add)
		}
		if perr == nil {
			inputLines = countLines(data)
			lineSrc = nil
		} else if auto {
			// It looked like a JSON document or a JUnit report and isn't
			// one (a log that starts with "[", JSON lines, a report too
			// damaged to read): read it as a log instead.
			fi, _ = lookupFormat("logs")
			g = newGrouper(rules, opt.Keep)
			if opt.Track {
				g.passIDs = map[string]bool{}
			}
			doc, summary = nil, nil
			lineSrc = io.MultiReader(bytes.NewReader(data), in)
		} else {
			return nil, perr
		}
	}
	if lineSrc != nil {
		ad := fi.new()
		if pt, ok := ad.(*pytest); ok {
			pt.addN = g.addN
		}
		lr := bufio.NewReaderSize(lineSrc, 64<<10)
		var n int64
		size := 0
		for {
			raw, err := readLine(lr)
			if len(raw) > 0 {
				n++
				c := clean(raw)
				// Keep small inputs whole, in case the reduction would not
				// be smaller than the input itself.
				if size += len(c) + 1; size <= budget {
					small = append(small, c)
				}
				ad.line(n, c, g.add)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
		}
		summary = ad.finish(g.add)
		inputLines = n
	}
	// Drain anything a whole-format parser didn't read, so the hash, the
	// count and the sink cover the entire input.
	if _, err := io.Copy(io.Discard, in); err != nil {
		return nil, err
	}

	res := &Result{Format: fi.name, Family: fi.family, Display: fi.display, InputLines: inputLines}
	res.InputBytes = count.n
	res.ID = refID(fi.name, opt.Keep, h.Sum(nil))
	refs := !opt.NoRefs

	var groups []*group
	if doc != nil {
		res.Records = map[string]int64{"error": int64(len(doc.errors))}
		res.Lines = map[string]int64{}
		res.Index = &Index{ID: res.ID, Format: fi.name, Lines: res.InputLines, Unit: "lines", Groups: []IndexGroup{}}
	} else {
		groups = g.finish()
		res.Records, res.Lines = g.records, g.lines
		res.Index = buildIndex(res, groups)
	}

	// renderAt renders the report within b bytes.
	renderAt := func(b int) (text string, overflow, whole bool, err error) {
		if doc != nil {
			text, overflow, err = renderJSON(res, doc, b, refs)
			return text, overflow, false, err
		}
		text, overflow, err = render(res, groups, summary, rules, b, refs)
		if int64(len(small)) == res.InputLines && res.InputLines > 0 {
			// The input is small: when showing it whole is no longer than
			// the reduction, show it whole.
			w := headerLine(res, refs) + ", shown whole\n" + strings.Join(small, "\n") + "\n"
			if len(w) <= b && (err != nil || len(w) <= len(text)) {
				return w, false, true, nil
			}
		}
		return text, overflow, false, err
	}

	if !opt.Track {
		text, overflow, whole, err := renderAt(budget)
		if err != nil {
			return nil, err
		}
		res.Text, res.Overflow, res.Whole = text, overflow, whole
		return res, nil
	}
	if err := trackRun(res, opt, groups, g.passIDs, doc, rules, budget, refs, renderAt); err != nil {
		return nil, err
	}
	return res, nil
}

// trackRun compares the reduction with the earlier runs and renders the
// report with what changed: one line when nothing did, otherwise the changes
// under the header and notes on the items.
func trackRun(res *Result, opt Options, groups []*group, passIDs map[string]bool, doc *jsonDoc, rules preserve, budget int, refs bool,
	renderAt func(int) (string, bool, bool, error)) error {
	tests := res.Family == FamilyTests
	bad, good := runItems(res, groups, passIDs, doc, rules)
	cur := &Run{ID: res.ID, Format: res.Format, Command: opt.Command, Exit: opt.Exit, Records: res.Records, Bad: map[string]RunItem{}, Full: true}
	cur.N = 1
	if n := len(opt.History); n > 0 {
		cur.N = opt.History[n-1].N + 1
	}
	if tests {
		cur.Good = packGood(good)
		cur.Anon = len(good) == 0
	}
	d := computeDelta(opt.History, cur, bad, good, tests)
	res.Run, res.Delta = cur, d

	if d.Unchanged && !opt.Full {
		if line := unchangedLine(res, d, refs); len(line) <= budget {
			res.Text, res.Overflow = line, d.Failing > 0
			return nil
		}
	}
	noteGroups(d, cur, groups, tests)
	// The changes take at most a third of the budget; past that each list
	// is cut short, and in the end only the summary line is left.
	for _, capN := range []int{1 << 30, 10, 5, 3, 1, 0} {
		block := blockLines(d, capN, refs)
		cost := 0
		for _, l := range block {
			cost += len(l) + 1
		}
		if capN > 0 && cost > budget/3 {
			continue
		}
		text, overflow, whole, err := renderAt(budget - cost)
		var be *BudgetError
		if errors.As(err, &be) {
			if capN == 0 {
				return &BudgetError{Budget: budget, Need: be.Need + cost}
			}
			continue
		}
		if err != nil {
			return err
		}
		nl := strings.IndexByte(text, '\n') + 1
		res.Text = text[:nl] + strings.Join(block, "\n") + "\n" + text[nl:]
		res.Overflow = overflow
		res.Whole = whole
		return nil
	}
	return &BudgetError{Budget: budget, Need: budget + 1}
}

// refID names a stored input. It covers everything that decides how lines
// are grouped and numbered (the format and the keep rules) as well as the
// input itself, so a reference from one run never points into the index of
// a run with different groups.
func refID(format string, keep []*regexp.Regexp, contentHash []byte) string {
	h := sha256.New()
	h.Write([]byte("abbreviatory/1\n" + format + "\n"))
	for _, re := range keep {
		h.Write([]byte(re.String()))
		h.Write([]byte{0})
	}
	h.Write([]byte{'\n'})
	h.Write(contentHash)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// CheckFormat reports whether Reduce accepts a format name.
func CheckFormat(name string) error {
	_, err := detect(nil, name)
	return err
}

// CheckPreserve reports whether Reduce accepts a preserve list.
func CheckPreserve(names []string) error {
	_, err := preserveRules(names)
	return err
}

type preserve struct {
	failures, errors, warnings bool
}

func (p preserve) has(k Kind) bool {
	switch k {
	case KindFail:
		return p.failures
	case KindError:
		return p.errors
	case KindWarn:
		return p.warnings
	}
	return false
}

func preserveRules(names []string) (preserve, error) {
	if names == nil {
		return preserve{true, true, true}, nil
	}
	var p preserve
	for _, n := range names {
		switch n {
		case "failures", "failure", "fail", "failed":
			p.failures = true
		case "errors", "error":
			p.errors = true
		case "warnings", "warning", "warn":
			p.warnings = true
		case "":
		default:
			return p, errors.New("unknown preserve value " + quote(n) + "; use errors, failures, warnings")
		}
	}
	return p, nil
}

// readLine reads one line including its newline. It returns at most the
// whole line however long it is; the caller cuts it for parsing.
func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	// A very long line: keep the first part for parsing and skip the rest.
	buf := append([]byte(nil), line...)
	for err == bufio.ErrBufferFull {
		line, err = r.ReadSlice('\n')
		if len(buf) < maxParseLine+1 {
			buf = append(buf, line...)
		}
	}
	if len(line) > 0 && line[len(line)-1] == '\n' && (len(buf) == 0 || buf[len(buf)-1] != '\n') {
		buf = append(buf, '\n')
	}
	return buf, err
}

func readWhole(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return data, err
	}
	if len(data) > limit {
		return data, errors.New("input is larger than " + humanBytes(int64(limit)) + ", the most this format reads whole; use -format logs to stream it")
	}
	return data, nil
}

func countLines(data []byte) int64 {
	n := int64(bytes.Count(data, []byte{'\n'}))
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// isBinary reports whether a sample is binary: it has NUL bytes, or more
// than one in ten bytes is not valid UTF-8 text.
func isBinary(sample []byte) bool {
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}
	bad := 0
	for i := 0; i < len(sample); {
		r, size := utf8.DecodeRune(sample[i:])
		if r == utf8.RuneError && size <= 1 {
			// A rune cut off by the end of the sample is not bad.
			if len(sample)-i < 4 && !utf8.FullRune(sample[i:]) {
				break
			}
			bad++
		} else if r < 0x20 && r != '\n' && r != '\r' && r != '\t' && r != 0x1b && r != '\f' && r != '\b' {
			bad++
		}
		i += size
	}
	return bad*10 > len(sample)
}
