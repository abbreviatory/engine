package abbreviatory

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// pending holds the output lines seen so far for one test or package.
type pending struct {
	detail []string
	lines  []Range
	first  int64
}

func (p *pending) add(n int64, text string, keep bool) {
	p.lines = addRange(p.lines, Range{n, n})
	if p.first == 0 {
		p.first = n
	}
	if keep && len(p.detail) <= maxDetail {
		p.detail = append(p.detail, text)
	}
}

// isGoTestMarker reports lines go test prints around a test's own output.
func isGoTestMarker(s string) bool {
	t := strings.TrimLeft(s, " ")
	for _, p := range []string{"=== RUN", "=== PAUSE", "=== CONT", "=== NAME", "--- PASS:", "--- FAIL:", "--- SKIP:"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// trimDetail drops leading and trailing blank lines.
func trimDetail(d []string) []string {
	for len(d) > 0 && strings.TrimSpace(d[0]) == "" {
		d = d[1:]
	}
	for len(d) > 0 && strings.TrimSpace(d[len(d)-1]) == "" {
		d = d[:len(d)-1]
	}
	return d
}

// ---- go test -json ----

type goTestJSON struct {
	tests    map[string]*pending // package + "\x00" + test
	order    []string
	pkgs     map[string]*pending
	pkgFails map[string]int // failed tests per package
	builds   map[string]*pending
}

func newGoTestJSON() *goTestJSON {
	return &goTestJSON{
		tests:    map[string]*pending{},
		pkgs:     map[string]*pending{},
		pkgFails: map[string]int{},
		builds:   map[string]*pending{},
	}
}

type goEvent struct {
	Action     string
	Package    string
	Test       string
	Output     string
	Elapsed    float64
	ImportPath string
}

func (g *goTestJSON) line(n int64, text string, emit func(Record)) {
	var ev goEvent
	if !strings.HasPrefix(strings.TrimSpace(text), "{") || json.Unmarshal([]byte(text), &ev) != nil || ev.Action == "" {
		// Plain text mixed in (go vet output, a wrapper script).
		emit(Record{Kind: levelOf(text), Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
		return
	}
	out := strings.TrimRight(ev.Output, "\r\n")
	switch ev.Action {
	case "build-output":
		p := g.get(g.builds, ev.ImportPath)
		p.add(n, out, true)
		return
	case "build-fail":
		p := g.get(g.builds, ev.ImportPath)
		p.add(n, "", false)
		emit(Record{Kind: KindError, ID: "build:" + ev.ImportPath, Head: "build failed: " + ev.ImportPath,
			Detail: trimDetail(dropEmpty(p.detail)), Lines: p.lines})
		delete(g.builds, ev.ImportPath)
		return
	}
	if ev.Test == "" {
		p := g.get(g.pkgs, ev.Package)
		switch ev.Action {
		case "pass", "fail", "skip":
			p.add(n, "", false)
			g.finishPackage(ev, p, emit)
			delete(g.pkgs, ev.Package)
		case "output":
			p.add(n, out, !isPackageSummary(out))
		default:
			p.add(n, "", false)
		}
		return
	}
	key := ev.Package + "\x00" + ev.Test
	p := g.tests[key]
	if p == nil {
		p = &pending{}
		g.tests[key] = p
		g.order = append(g.order, key)
	}
	switch ev.Action {
	case "output":
		p.add(n, out, !isGoTestMarker(out))
	case "pass", "fail", "skip":
		p.add(n, "", false)
		kind := map[string]Kind{"pass": KindPass, "fail": KindFail, "skip": KindSkip}[ev.Action]
		r := Record{Kind: kind, ID: ev.Package + "." + ev.Test,
			Head:  ev.Test + " (" + ev.Package + ", " + strconv.FormatFloat(ev.Elapsed, 'f', 2, 64) + "s)",
			Lines: p.lines}
		if kind != KindPass {
			r.Detail = trimDetail(p.detail)
		}
		if kind == KindFail {
			g.pkgFails[ev.Package]++
		}
		emit(r)
		delete(g.tests, key)
	default:
		p.add(n, "", false)
	}
}

func (g *goTestJSON) get(m map[string]*pending, k string) *pending {
	p := m[k]
	if p == nil {
		p = &pending{}
		m[k] = p
	}
	return p
}

// isPackageSummary matches the lines go test prints at the end of a package
// ("ok", "FAIL", "PASS", coverage), which add nothing to a failure.
func isPackageSummary(s string) bool {
	t := strings.TrimSpace(s)
	return t == "PASS" || t == "FAIL" || strings.HasPrefix(t, "ok  ") || strings.HasPrefix(t, "FAIL\t") ||
		strings.HasPrefix(t, "coverage:") || strings.HasPrefix(t, "?   \t")
}

func (g *goTestJSON) finishPackage(ev goEvent, p *pending, emit func(Record)) {
	el := strconv.FormatFloat(ev.Elapsed, 'f', 2, 64) + "s"
	switch {
	case ev.Action == "fail" && g.pkgFails[ev.Package] == 0:
		// The package failed with no failing test: a build error, a
		// panic outside a test, a timeout, TestMain exiting.
		emit(Record{Kind: KindError, ID: "pkg:" + ev.Package, Head: "package failed: " + ev.Package + " (" + el + ")",
			Detail: trimDetail(p.detail), Lines: p.lines})
	case ev.Action == "fail":
		emit(Record{Kind: KindInfo, ID: "pkg:" + ev.Package, Head: "FAIL " + ev.Package + " (" + el + ")", Lines: p.lines})
	case ev.Action == "skip":
		emit(Record{Kind: KindInfo, ID: "pkg:" + ev.Package, Head: "? " + ev.Package + " [no test files]", Lines: p.lines})
	default:
		emit(Record{Kind: KindInfo, ID: "pkg:" + ev.Package, Head: "ok " + ev.Package + " (" + el + ")", Lines: p.lines})
	}
}

func (g *goTestJSON) finish(emit func(Record)) []string {
	// Tests that started and never finished: the run stopped (a panic in
	// another test, a timeout, the process killed).
	for _, key := range g.order {
		p := g.tests[key]
		if p == nil {
			continue
		}
		parts := strings.SplitN(key, "\x00", 2)
		emit(Record{Kind: KindFail, ID: parts[0] + "." + parts[1], Head: parts[1] + " (" + parts[0] + ", no result: the run stopped)",
			Detail: trimDetail(p.detail), Lines: p.lines})
	}
	for _, m := range []map[string]*pending{g.pkgs, g.builds} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := m[k]
			emit(Record{Kind: KindInfo, ID: "pkg:" + k, Head: "output of " + k, Detail: trimDetail(p.detail), Lines: p.lines})
		}
	}
	return nil
}

func dropEmpty(d []string) []string {
	var out []string
	for _, s := range d {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---- go test (text) ----

type goTestText struct {
	tests   map[string]*pending
	order   []string
	cur     string
	result  *Record // last --- FAIL/PASS/SKIP, collecting indented lines
	indent  int
	block   *Record // "# pkg" build errors or a panic
	blockOn bool
}

func newGoTestText() *goTestText { return &goTestText{tests: map[string]*pending{}} }

func (g *goTestText) flushResult(emit func(Record)) {
	if g.result != nil {
		g.result.Detail = trimDetail(g.result.Detail)
		emit(*g.result)
		g.result = nil
	}
}

func (g *goTestText) flushBlock(emit func(Record)) {
	if g.block != nil {
		g.block.Detail = trimDetail(g.block.Detail)
		emit(*g.block)
		g.block = nil
	}
}

func (g *goTestText) test(name string) *pending {
	p := g.tests[name]
	if p == nil {
		p = &pending{}
		g.tests[name] = p
		g.order = append(g.order, name)
	}
	return p
}

func (g *goTestText) line(n int64, text string, emit func(Record)) {
	t := strings.TrimLeft(text, " ")
	ind := len(text) - len(t)

	// Indented lines after a result belong to it (the failure messages of
	// a non-verbose run).
	if g.result != nil && ind > g.indent && !strings.HasPrefix(t, "--- ") && !strings.HasPrefix(t, "=== ") {
		g.result.Detail = append(g.result.Detail, strings.TrimRight(text, " \t"))
		g.result.addLine(n)
		return
	}
	if g.block != nil {
		if g.blockOn && !isGoPackageLine(t) && !strings.HasPrefix(t, "=== RUN") && !strings.HasPrefix(t, "--- ") {
			g.block.Detail = append(g.block.Detail, strings.TrimRight(text, " \t"))
			g.block.addLine(n)
			return
		}
		g.flushBlock(emit)
	}

	for _, m := range []string{"=== RUN", "=== CONT", "=== PAUSE", "=== NAME"} {
		if strings.HasPrefix(t, m) {
			g.flushResult(emit)
			name := strings.TrimSpace(t[len(m):])
			g.cur = name
			g.test(name).add(n, "", false)
			return
		}
	}
	for _, m := range []struct {
		p string
		k Kind
	}{{"--- FAIL: ", KindFail}, {"--- PASS: ", KindPass}, {"--- SKIP: ", KindSkip}} {
		if strings.HasPrefix(t, m.p) {
			g.flushResult(emit)
			rest := t[len(m.p):]
			name := rest
			if i := strings.Index(rest, " ("); i >= 0 {
				name = rest[:i]
			}
			p := g.test(name)
			p.add(n, "", false)
			r := &Record{Kind: m.k, ID: name, Head: rest, Lines: p.lines}
			if m.k != KindPass {
				r.Detail = p.detail
			}
			delete(g.tests, name)
			if g.cur == name {
				g.cur = ""
			}
			g.result = r
			g.indent = ind
			if m.k == KindPass {
				g.flushResult(emit)
			}
			return
		}
	}
	g.flushResult(emit)

	switch {
	case isGoPackageLine(t):
		k := KindInfo
		if strings.Contains(t, "[build failed]") || strings.Contains(t, "[setup failed]") {
			k = KindError
		}
		emit(Record{Kind: k, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
		g.cur = ""
		return
	case strings.HasPrefix(t, "# ") && ind == 0:
		g.block = &Record{Kind: KindError, Head: "build failed: " + strings.TrimSpace(t[2:]), Lines: []Range{{n, n}}}
		g.blockOn = true
		return
	case strings.HasPrefix(t, "panic: ") || strings.HasPrefix(t, "fatal error: "):
		g.block = &Record{Kind: KindError, Head: strings.TrimRight(t, " \t"), Lines: []Range{{n, n}}}
		if g.cur != "" {
			g.block.Head += " (in " + g.cur + ")"
		}
		g.blockOn = true
		return
	}
	if g.cur != "" {
		g.test(g.cur).add(n, strings.TrimRight(text, " \t"), true)
		return
	}
	emit(Record{Kind: levelOf(text), Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
}

// isGoPackageLine matches go test's per-package result lines.
func isGoPackageLine(t string) bool {
	return t == "PASS" || t == "FAIL" || strings.HasPrefix(t, "ok  \t") || strings.HasPrefix(t, "ok \t") ||
		strings.HasPrefix(t, "ok  ") || strings.HasPrefix(t, "FAIL\t") || strings.HasPrefix(t, "?   \t")
}

func (g *goTestText) finish(emit func(Record)) []string {
	g.flushResult(emit)
	g.flushBlock(emit)
	for _, name := range g.order {
		p := g.tests[name]
		if p == nil {
			continue
		}
		emit(Record{Kind: KindFail, ID: name, Head: name + " (no result: the run stopped)", Detail: trimDetail(p.detail), Lines: p.lines})
	}
	return nil
}
