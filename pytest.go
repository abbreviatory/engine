package abbreviatory

import (
	"regexp"
	"strconv"
	"strings"
)

// pytest reads pytest's terminal output, verbose or not: the FAILURES and
// ERRORS sections, the short test summary, the per-test lines of -v, the
// warnings summary and the final count.
type pytest struct {
	mode       string // "", "failures", "errors", "summary", "warnings"
	sections   []*pySection
	cur        *pySection
	summary    []pyResult // FAILED / ERROR lines from the short summary
	verbose    map[string]pyResult
	vorder     []string
	final      []string
	finalN     map[string]int64
	sawVerbose bool
	warn       *Record
}

type pySection struct {
	name   string
	errors bool // from the ERRORS section
	detail []string
	lines  []Range
	used   bool
}

type pyResult struct {
	status, nodeid, msg string
	line                int64
}

var (
	pyBanner   = regexp.MustCompile(`^=+ (.*?) =+$`)
	pySection_ = regexp.MustCompile(`^_{3,} (.+?) _{3,}$`)
	pyVerbose  = regexp.MustCompile(`^(\S+::\S.*?) (PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS)\b(.*)$`)
	pyXdist    = regexp.MustCompile(`^\[gw\d+\] \[\s*\d+%\] (PASSED|FAILED|ERROR|SKIPPED|XFAIL|XPASS) (\S+::\S+)(.*)$`)
	pyShort    = regexp.MustCompile(`^(FAILED|ERROR) (.+?)(?: - (.*))?$`)
	pyCount    = regexp.MustCompile(`(\d+) (passed|failed|errors?|skipped|xfailed|xpassed|warnings?|deselected|rerun)`)
)

func newPytest() *pytest {
	return &pytest{verbose: map[string]pyResult{}, finalN: map[string]int64{}}
}

func (p *pytest) line(n int64, text string, emit func(Record)) {
	t := strings.TrimRight(text, " \t")
	if m := pyBanner.FindStringSubmatch(t); m != nil {
		p.endWarn(emit)
		title := m[1]
		switch {
		case title == "FAILURES":
			p.mode = "failures"
		case title == "ERRORS":
			p.mode = "errors"
		case title == "short test summary info":
			p.mode = "summary"
		case title == "warnings summary":
			p.mode = "warnings"
		default:
			p.mode = ""
		}
		p.cur = nil
		if pyCount.MatchString(title) && strings.Contains(title, " in ") {
			p.final = append(p.final, title)
			for _, c := range pyCount.FindAllStringSubmatch(title, -1) {
				v, _ := strconv.ParseInt(c[1], 10, 64)
				key := strings.TrimSuffix(c[2], "s")
				if c[2] == "passed" || c[2] == "failed" || c[2] == "skipped" || c[2] == "xfailed" || c[2] == "xpassed" {
					key = c[2]
				}
				p.finalN[key] += v
			}
		}
		emit(Record{Kind: KindInfo, Head: t, Lines: []Range{{n, n}}})
		return
	}
	switch p.mode {
	case "failures", "errors":
		if m := pySection_.FindStringSubmatch(t); m != nil {
			p.cur = &pySection{name: m[1], errors: p.mode == "errors"}
			p.cur.lines = addRange(p.cur.lines, Range{n, n})
			p.sections = append(p.sections, p.cur)
			return
		}
		if p.cur != nil {
			if len(p.cur.detail) <= maxDetail {
				p.cur.detail = append(p.cur.detail, t)
			}
			p.cur.lines = addRange(p.cur.lines, Range{n, n})
			return
		}
	case "summary":
		if m := pyShort.FindStringSubmatch(t); m != nil {
			p.summary = append(p.summary, pyResult{status: m[1], nodeid: m[2], msg: m[3], line: n})
			return
		}
	case "warnings":
		if p.warningLine(n, t, emit) {
			return
		}
	}
	if m := pyVerbose.FindStringSubmatch(t); m != nil {
		p.result(n, m[2], m[1], emit)
		return
	}
	if m := pyXdist.FindStringSubmatch(t); m != nil {
		p.result(n, m[1], m[2], emit)
		return
	}
	emit(Record{Kind: KindInfo, Head: t, Lines: []Range{{n, n}}})
}

func (p *pytest) result(n int64, status, nodeid string, emit func(Record)) {
	p.sawVerbose = true
	switch status {
	case "PASSED", "XPASS":
		emit(Record{Kind: KindPass, ID: nodeid, Head: nodeid, Lines: []Range{{n, n}}})
	case "SKIPPED", "XFAIL":
		emit(Record{Kind: KindSkip, ID: nodeid, Head: nodeid + " " + strings.ToLower(status), Lines: []Range{{n, n}}})
	default:
		if _, ok := p.verbose[nodeid]; !ok {
			p.vorder = append(p.vorder, nodeid)
		}
		p.verbose[nodeid] = pyResult{status: status, nodeid: nodeid, line: n}
	}
}

// warningLine handles the warnings summary: an unindented location line
// starts an entry, indented lines carry the warning itself.
func (p *pytest) warningLine(n int64, t string, emit func(Record)) bool {
	if t == "" {
		p.endWarn(emit)
		emit(Record{Kind: KindInfo, Head: "(blank line)", Lines: []Range{{n, n}}})
		return true
	}
	if strings.HasPrefix(t, "-- Docs:") {
		p.endWarn(emit)
		return false
	}
	if t[0] == ' ' {
		if p.warn == nil {
			return false
		}
		s := strings.TrimSpace(t)
		if !strings.Contains(p.warn.Head, ": ") && strings.Contains(s, "Warning") {
			p.warn.Head += ": " + s
		} else {
			p.warn.Detail = append(p.warn.Detail, t)
		}
		p.warn.addLine(n)
		return true
	}
	p.endWarn(emit)
	p.warn = &Record{Kind: KindWarn, Head: t, Lines: []Range{{n, n}}}
	return true
}

func (p *pytest) endWarn(emit func(Record)) {
	if p.warn != nil {
		emit(*p.warn)
		p.warn = nil
	}
}

// sectionFor finds the FAILURES or ERRORS section for a node ID. Section
// titles are the test name with any class ("TestCls.test_x", "test_y[2]"),
// or "ERROR at setup of test_x", or "ERROR collecting tests/x.py".
func (p *pytest) sectionFor(nodeid string, errors bool) *pySection {
	parts := strings.Split(nodeid, "::")
	short := nodeid
	if len(parts) > 1 {
		short = strings.Join(parts[1:], ".")
	}
	for _, s := range p.sections {
		if s.used || s.errors != errors {
			continue
		}
		name := s.name
		for _, pre := range []string{"ERROR at setup of ", "ERROR at teardown of ", "ERROR collecting "} {
			name = strings.TrimPrefix(name, pre)
		}
		if name == short || name == nodeid || strings.HasSuffix(short, "."+name) || name == parts[0] {
			s.used = true
			return s
		}
	}
	return nil
}

func (p *pytest) finish(emit func(Record)) []string {
	p.endWarn(emit)
	seen := map[string]bool{}
	emitFail := func(status, nodeid, msg string, extra []Range) {
		kind := KindFail
		if status == "ERROR" {
			kind = KindError
		}
		head := nodeid
		if msg != "" {
			head += " - " + msg
		}
		r := Record{Kind: kind, ID: nodeid, Head: head}
		if s := p.sectionFor(nodeid, kind == KindError); s != nil {
			r.Detail = trimDetail(s.detail)
			r.Lines = append(r.Lines, s.lines...)
		}
		r.Lines = append(r.Lines, extra...)
		if v, ok := p.verbose[nodeid]; ok {
			r.Lines = append(r.Lines, Range{v.line, v.line})
		}
		r.Lines = mergeRanges(r.Lines)
		seen[nodeid] = true
		emit(r)
	}
	for _, s := range p.summary {
		if seen[s.nodeid] {
			continue
		}
		emitFail(s.status, s.nodeid, s.msg, []Range{{s.line, s.line}})
	}
	for _, id := range p.vorder {
		if seen[id] {
			continue
		}
		emitFail(p.verbose[id].status, id, "", nil)
	}
	for _, s := range p.sections {
		if s.used {
			continue
		}
		kind := KindFail
		if s.errors {
			kind = KindError
		}
		emit(Record{Kind: kind, ID: s.name, Head: s.name, Detail: trimDetail(s.detail), Lines: s.lines})
	}
	// Without -v there are no per-test pass lines; take the counts from
	// pytest's own summary so the totals are right.
	if !p.sawVerbose {
		for i := int64(0); i < p.finalN["passed"]+p.finalN["xpassed"]; i++ {
			emit(Record{Kind: KindPass, Head: "passed (counted from pytest's summary)"})
		}
		for i := int64(0); i < p.finalN["skipped"]+p.finalN["xfailed"]; i++ {
			emit(Record{Kind: KindSkip, Head: "skipped (counted from pytest's summary)"})
		}
	}
	var out []string
	for _, f := range p.final {
		out = append(out, "pytest: "+f)
	}
	return out
}
