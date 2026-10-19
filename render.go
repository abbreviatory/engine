package abbreviatory

import (
	"sort"
	"strconv"
	"strings"
)

// maxShowDetail is how many detail lines one item shows at most.
const maxShowDetail = 40

// section is a heading with the groups listed under it.
type section struct {
	key       string // selector for references: fail, error, keep, warn
	title     string
	noun      string // for the "not shown" line
	preserved bool
	groups    []*group
	shown     []bool
	detail    []int // detail lines shown per group (0 = compact)
	any       bool
}

func render(res *Result, groups []*group, summary []string, rules preserve, budget int, refs bool) (string, bool, error) {
	ref := func(sel string) string {
		if !refs {
			return ""
		}
		if sel == "" {
			return " [ref " + res.ID + "]"
		}
		return " [ref " + res.ID + ":" + sel + "]"
	}

	head := []string{headerLine(res, refs)}
	if res.Family == FamilyListing || res.Family == FamilyHTTP {
		// The listing's own counts.
		for _, s := range summary {
			head = append(head, cut(s, maxHead))
		}
	} else {
		head = append(head, countSummary(res, groups)...)
		for i, s := range summary {
			if i == 3 {
				break
			}
			head = append(head, "runner: "+cut(s, maxHead))
		}
	}

	tests := res.Family == FamilyTests
	var fail, errs, kept, warn, other []*group
	var passGroups, skipGroups []*group
	for _, g := range groups {
		switch {
		case g.kind == KindFail && rules.failures:
			fail = append(fail, g)
		case g.kind == KindError && rules.errors:
			errs = append(errs, g)
		case g.kept:
			kept = append(kept, g)
		case g.kind == KindPass:
			passGroups = append(passGroups, g)
		case g.kind == KindSkip:
			skipGroups = append(skipGroups, g)
		case g.kind == KindWarn && rules.warnings:
			warn = append(warn, g)
		default:
			other = append(other, g)
		}
	}
	byCount := func(gs []*group) {
		sort.SliceStable(gs, func(i, j int) bool {
			if gs[i].kind != gs[j].kind {
				return gs[i].kind > gs[j].kind
			}
			if gs[i].count != gs[j].count {
				return gs[i].count > gs[j].count
			}
			return gs[i].idx < gs[j].idx
		})
	}
	byCount(warn)
	if res.Family == FamilyHTTP {
		// A response reads best in its own order: status, headers, body.
		sort.SliceStable(other, func(i, j int) bool { return other[i].idx < other[j].idx })
	} else {
		byCount(other)
	}
	// Compared with earlier runs: new failures first, then flaky ones, then
	// those failing before too. Without history every rank is 0.
	byRank := func(gs []*group) {
		sort.SliceStable(gs, func(i, j int) bool { return gs[i].rank < gs[j].rank })
	}
	byRank(fail)
	byRank(errs)

	failNoun := "failures"
	if tests {
		failNoun = "failed tests"
	}
	secs := []*section{
		{key: "fail", title: "FAILED", noun: failNoun, preserved: true, groups: fail},
		{key: "error", title: "ERRORS", noun: "errors", preserved: true, groups: errs},
		{key: "keep", title: "MATCHED --keep", noun: "matches", preserved: true, groups: kept},
		{key: "warn", title: "WARNINGS", noun: "warnings", preserved: true, groups: warn},
		{key: "", title: "OTHER", noun: "", groups: other},
	}
	for _, s := range secs {
		s.shown = make([]bool, len(s.groups))
		s.detail = make([]int, len(s.groups))
	}

	used := 0
	for _, l := range head {
		used += len(l) + 1
	}
	footMax := 0
	for _, l := range footer(secs, passGroups, skipGroups, tests, ref, true) {
		footMax += len(l) + 1
	}
	if used+footMax > budget {
		return "", false, &BudgetError{Budget: budget, Need: used + footMax}
	}
	room := budget - used - footMax

	compact := func(s *section, g *group) string { return itemLine(s, g, ref) }

	// Pass 1: every preserved item gets its one-line form, in order.
	for _, s := range secs[:4] {
		for i, g := range s.groups {
			cost := len(compact(s, g)) + 1
			if !s.any {
				cost += len(sectionTitle(s, tests)) + 1
			}
			if cost <= room {
				room -= cost
				s.shown[i] = true
				s.any = true
			}
		}
	}
	// Pass 2: shown preserved items get detail lines in rounds, so every
	// item gets a few before any gets many.
	for _, limit := range []int{2, 6, 16, maxShowDetail} {
		for _, s := range secs[:4] {
			for i, g := range s.groups {
				if !s.shown[i] || s.detail[i] >= len(g.ex.Detail) {
					continue
				}
				want := limit
				if want > len(g.ex.Detail) {
					want = len(g.ex.Detail)
				}
				have := detailCost(g, s.detail[i])
				for k := want; k > s.detail[i]; k-- {
					if extra := detailCost(g, k) - have; extra <= room {
						room -= extra
						s.detail[i] = k
						break
					}
				}
			}
		}
	}
	// Pass 3: other groups, most frequent first, while they fit.
	s := secs[4]
	misses := 0
	for i, g := range s.groups {
		cost := len(compact(s, g)) + 1
		if !s.any {
			cost += len(sectionTitle(s, tests)) + 1
		}
		if cost <= room {
			room -= cost
			s.shown[i] = true
			s.any = true
			misses = 0
		} else if misses++; misses > 200 {
			break
		}
	}

	// Pass 4, for HTTP: the shown groups' own detail (a JSON body's
	// outline), in rounds.
	if res.Family == FamilyHTTP {
		for _, limit := range []int{6, 16, maxShowDetail} {
			for i, g := range s.groups {
				if !s.shown[i] || s.detail[i] >= len(g.ex.Detail) {
					continue
				}
				want := min(limit, len(g.ex.Detail))
				have := detailCost(g, s.detail[i])
				for k := want; k > s.detail[i]; k-- {
					if extra := detailCost(g, k) - have; extra <= room {
						room -= extra
						s.detail[i] = k
						break
					}
				}
			}
		}
	}

	var out []string
	out = append(out, head...)
	overflow := false
	for _, s := range secs {
		if s.any {
			out = append(out, sectionTitle(s, tests))
		}
		for i, g := range s.groups {
			if !s.shown[i] {
				if s.preserved {
					overflow = true
				}
				continue
			}
			out = append(out, compact(s, g))
			out = append(out, detailLines(g, s.detail[i])...)
		}
	}
	out = append(out, footer(secs, passGroups, skipGroups, tests, ref, false)...)
	text := strings.Join(out, "\n") + "\n"
	if len(text) > budget {
		// The accounting above makes this impossible; fail loudly rather
		// than break the promise.
		return "", false, &BudgetError{Budget: budget, Need: len(text)}
	}
	return text, overflow, nil
}

func headerLine(res *Result, refs bool) string {
	s := "[abbreviatory] " + res.Display + ", " + plural(res.InputLines, "line", "lines") + " (" + humanBytes(res.InputBytes) + ")"
	if refs {
		s += ", ref " + res.ID
	}
	return s
}

func countSummary(res *Result, groups []*group) []string {
	if res.Family == FamilyTests {
		r := res.Records
		s := "tests: " + group3(r["pass"]) + " passed, " + group3(r["fail"]) + " failed, " + group3(r["skip"]) + " skipped"
		if r["error"] > 0 {
			s += "; " + plural(r["error"], "error", "errors")
		}
		return []string{s}
	}
	l := res.Lines
	distinct := 0
	for _, g := range groups {
		if !g.more {
			distinct++
		}
	}
	s := "lines: " + group3(l["error"]+l["fail"]) + " error, " + group3(l["warn"]) + " warn, " + group3(l["info"]+l["pass"]+l["skip"]) + " other; " + plural(int64(distinct), "distinct message", "distinct messages")
	return []string{s}
}

func sectionTitle(s *section, tests bool) string {
	var distinct, lines int64
	for _, g := range s.groups {
		distinct++
		lines += g.lines
	}
	if s.key == "fail" || tests {
		var n int64
		for _, g := range s.groups {
			n += g.count
		}
		return s.title + " (" + group3(n) + "):"
	}
	return s.title + " (" + plural(distinct, "kind", "kinds") + ", " + plural(lines, "line", "lines") + "):"
}

func itemLine(s *section, g *group, ref func(string) string) string {
	var b strings.Builder
	b.WriteString("- ")
	if g.count > 1 {
		b.WriteString("x" + strconv.FormatInt(g.count, 10) + " ")
	}
	if s.key == "" && (g.kind == KindFail || g.kind == KindError || g.kind == KindWarn) {
		b.WriteString(strings.ToUpper(g.kind.String()) + ": ")
	}
	head := g.ex.Head
	if g.count > 1 && g.ex.GroupHead != "" {
		head = g.ex.GroupHead
	}
	b.WriteString(cut(head, maxHead))
	if g.note != "" {
		b.WriteString(" (" + g.note + ")")
	}
	b.WriteString(ref("g" + strconv.Itoa(g.idx)))
	return b.String()
}

// detailLines shows k of a group's detail lines: when they don't all fit,
// the first and last ones (where stack traces keep the message and the
// cause), with a count of those in between. Common indentation is removed.
func detailLines(g *group, k int) []string {
	if k == 0 {
		return nil
	}
	d := g.ex.Detail
	if k >= len(d) {
		k = len(d)
	}
	head := k
	gap := len(d) - k
	if gap > 0 {
		head = (k + 1) / 2
	}
	pick := append(append([]string{}, d[:head]...), d[len(d)-(k-head):]...)
	ind := commonIndent(pick)
	var out []string
	for i, l := range pick {
		if i == head && gap > 0 {
			out = append(out, "    ... ("+plural(int64(gap), "line", "lines")+" left out)")
		}
		if len(l) >= ind {
			l = l[ind:]
		}
		out = append(out, "    "+cut(l, maxHead))
	}
	if gap > 0 && head == len(pick) {
		out = append(out, "    ... ("+plural(int64(gap), "more line", "more lines")+")")
	}
	return out
}

// commonIndent is the number of leading spaces all non-blank lines share.
func commonIndent(ls []string) int {
	min := -1
	for _, l := range ls {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " "))
		if min < 0 || n < min {
			min = n
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

func detailCost(g *group, k int) int {
	n := 0
	for _, l := range detailLines(g, k) {
		n += len(l) + 1
	}
	return n
}

// footer accounts for everything not shown. With worst set, it is computed
// as if nothing were shown, which is the longest it can be.
func footer(secs []*section, pass, skip []*group, tests bool, ref func(string) string, worst bool) []string {
	var out []string
	for _, s := range secs[:4] {
		var omitted, total int64
		for i, g := range s.groups {
			n := int64(1)
			if s.key == "fail" {
				n = g.count
			}
			total += n
			if worst || !s.shown[i] {
				omitted += n
			}
		}
		if omitted == 0 {
			continue
		}
		unit := "kinds"
		if s.key == "fail" {
			unit = ""
		}
		line := "not shown: " + group3(omitted) + " of " + group3(total) + " " + s.noun
		if unit != "" {
			line = "not shown: " + group3(omitted) + " of " + group3(total) + " kinds of " + s.noun
		}
		out = append(out, line+ref(s.key))
	}
	// Other groups, by kind.
	other := secs[4]
	for _, k := range []Kind{KindFail, KindError, KindWarn, KindInfo} {
		var lines, distinct int64
		for i, g := range other.groups {
			if g.kind != k || (!worst && other.shown[i]) {
				continue
			}
			lines += g.lines
			distinct++
		}
		if distinct == 0 {
			continue
		}
		out = append(out, "not shown: "+plural(lines, k.String()+" line", k.String()+" lines")+" ("+plural(distinct, "kind", "kinds")+")"+ref(k.String()))
	}
	for _, p := range []struct {
		gs   []*group
		kind string
		word string
	}{{pass, "pass", "passed"}, {skip, "skip", "skipped"}} {
		var recs, lines int64
		for _, g := range p.gs {
			recs += g.count
			lines += g.lines
		}
		if recs == 0 {
			continue
		}
		r := ref(p.kind)
		if lines == 0 {
			// Counted from the runner's summary: no lines to point to.
			r = ""
		}
		if tests {
			out = append(out, "not shown: "+plural(recs, p.word+" test", p.word+" tests")+r)
		} else {
			out = append(out, "not shown: "+plural(lines, p.kind+" line", p.kind+" lines")+r)
		}
	}
	return out
}
