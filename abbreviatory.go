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
const Version = "0.1.0"

// DefaultBudget is the output size in bytes when none is given.
const DefaultBudget = 4096

// MinBudget is the smallest budget accepted. Below it there is no room for
// the header, the counts and the references that account for what is left
// out.
const MinBudget = 512

// maxWhole is the largest input read whole (JSON documents, JUnit XML).
// Larger inputs of those formats are an error; logs and test output stream
// and have no limit.
const maxWhole = 256 << 20

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
	sample, _ := br.Peek(64 << 10)
	if isBinary(sample) {
		return nil, ErrBinary
	}
	fi, err := detect(sample, opt.Format)
	if err != nil {
		return nil, err
	}

	h := sha256.New()
	h.Write([]byte(fi.name))
	h.Write([]byte{'\n'})
	var count countWriter
	writers := []io.Writer{h, &count}
	if opt.Sink != nil {
		writers = append(writers, opt.Sink)
	}
	in := io.TeeReader(br, io.MultiWriter(writers...))

	res := &Result{Format: fi.name, Family: fi.family, Display: fi.display}
	g := newGrouper(rules, opt.Keep)
	var summary []string
	var doc *jsonDoc
	var small []string

	switch {
	case fi.name == "json":
		data, err := readWhole(in)
		if err != nil {
			return nil, err
		}
		res.InputLines = countLines(data)
		doc, err = parseJSONDoc(data)
		if err != nil {
			return nil, err
		}
	case fi.name == "junit":
		data, err := readWhole(in)
		if err != nil {
			return nil, err
		}
		res.InputLines = countLines(data)
		g.bytes = true
		summary, err = parseJUnit(data, g.add)
		if err != nil {
			return nil, err
		}
	default:
		ad := fi.new()
		lr := bufio.NewReaderSize(in, 64<<10)
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
		res.InputLines = n
	}
	// Drain anything a whole-format parser didn't read, so the hash, the
	// count and the sink cover the entire input.
	if _, err := io.Copy(io.Discard, in); err != nil {
		return nil, err
	}

	res.InputBytes = count.n
	res.ID = hex.EncodeToString(h.Sum(nil))[:12]
	refs := !opt.NoRefs

	if doc != nil {
		text, overflow, err := renderJSON(res, doc, budget, refs)
		if err != nil {
			return nil, err
		}
		res.Text, res.Overflow = text, overflow
		res.Records = map[string]int64{"error": int64(len(doc.errors))}
		res.Lines = map[string]int64{}
		res.Index = &Index{ID: res.ID, Format: fi.name, Lines: res.InputLines, Unit: "lines", Groups: []IndexGroup{}}
		return res, nil
	}

	groups := g.finish()
	res.Records, res.Lines = g.records, g.lines
	res.Index = buildIndex(res, groups)
	text, overflow, err := render(res, groups, summary, rules, budget, refs)
	if int64(len(small)) == res.InputLines && res.InputLines > 0 {
		// The input is small: when showing it whole is no longer than the
		// reduction, show it whole.
		whole := headerLine(res, refs) + ", shown whole\n" + strings.Join(small, "\n") + "\n"
		if len(whole) <= budget && (err != nil || len(whole) <= len(text)) {
			res.Text, res.Whole = whole, true
			return res, nil
		}
	}
	if err != nil {
		return nil, err
	}
	res.Text, res.Overflow = text, overflow
	return res, nil
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

func readWhole(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxWhole+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxWhole {
		return nil, errors.New("input is larger than " + humanBytes(maxWhole) + "; JSON documents and JUnit reports are read whole and are limited to that size")
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
