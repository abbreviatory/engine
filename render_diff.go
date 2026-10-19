package abbreviatory

import (
	"strconv"
	"strings"
)

// renderDiff renders a diff: the summary, preserved hunks (conflict markers,
// trailing whitespace, keep rules) as the other formats show them, the
// commits, every file with its counts and a reference to its lines, then
// the changed lines of each hunk, shared out in rounds. The same change in
// several places is shown once with a count; generated files are listed but
// their changes aren't spelled out.
func renderDiff(res *Result, groups []*group, files []diffFile, summary []string, rules preserve, budget int, refs bool) (string, bool, error) {
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
	head = append(head, summary...)

	skip := map[string]bool{} // files whose changes aren't spelled out
	for _, f := range files {
		if f.generated || f.binary {
			skip[f.path] = true
		}
	}
	var fail, errs, kept, warn, commits, hunks []*group
	skipped := 0
	for _, g := range groups {
		switch {
		case g.kind == KindFail && rules.failures:
			fail = append(fail, g)
		case g.kind == KindError && rules.errors:
			errs = append(errs, g)
		case g.kept:
			kept = append(kept, g)
		case g.kind == KindWarn && rules.warnings:
			warn = append(warn, g)
		case g.more:
		case strings.HasPrefix(g.ex.ID, "commit:"):
			commits = append(commits, g)
		case strings.HasPrefix(g.ex.ID, "hunk:"):
			if skip[strings.TrimPrefix(g.ex.ID, "hunk:")] {
				skipped++
				continue
			}
			hunks = append(hunks, g)
		}
	}
	secs := []*section{
		{key: "fail", title: "FAILED", noun: "failures", preserved: true, groups: fail},
		{key: "error", title: "ERRORS", noun: "errors", preserved: true, groups: errs},
		{key: "keep", title: "MATCHED --keep", noun: "matches", preserved: true, groups: kept},
		{key: "warn", title: "WARNINGS", noun: "warnings", preserved: true, groups: warn},
		{key: "", title: "OTHER", groups: nil},
	}
	for _, s := range secs {
		s.shown = make([]bool, len(s.groups))
		s.detail = make([]int, len(s.groups))
	}
	commitShown := make([]bool, len(commits))
	fileShown := make([]bool, len(files))
	hunkShown := make([]bool, len(hunks))
	hunkDetail := make([]int, len(hunks))

	extra := func(worst bool) []string {
		var out []string
		var nc, nf, nh int
		for i := range commits {
			if worst || !commitShown[i] {
				nc++
			}
		}
		for i := range files {
			if worst || !fileShown[i] {
				nf++
			}
		}
		for i := range hunks {
			if worst || !hunkShown[i] {
				nh++
			}
		}
		if nc > 0 {
			out = append(out, "not shown: "+group3(int64(nc))+" of "+group3(int64(len(commits)))+" commits"+ref(""))
		}
		if nf > 0 {
			out = append(out, "not shown: "+group3(int64(nf))+" of "+group3(int64(len(files)))+" files"+ref(""))
		}
		if nh > 0 || skipped > 0 {
			s := "not shown: " + group3(int64(nh)) + " of " + group3(int64(len(hunks))) + " changes"
			if skipped > 0 {
				s += ", and " + plural(int64(skipped), "change", "changes") + " in generated or binary files"
			}
			out = append(out, s+ref(""))
		}
		return out
	}
	used := 0
	for _, l := range head {
		used += len(l) + 1
	}
	footMax := 0
	for _, l := range append(footer(secs, nil, nil, false, ref, true), extra(true)...) {
		footMax += len(l) + 1
	}
	if used+footMax > budget {
		return "", false, &BudgetError{Budget: budget, Need: used + footMax}
	}
	room := budget - used - footMax
	take := func(cost int) bool {
		if cost <= room {
			room -= cost
			return true
		}
		return false
	}

	// 1. Preserved items, one line each.
	for _, s := range secs[:4] {
		for i, g := range s.groups {
			cost := len(itemLine(s, g, ref)) + 1
			if !s.any {
				cost += len(sectionTitle(s, false)) + 1
			}
			if take(cost) {
				s.shown[i], s.any = true, true
			}
		}
	}
	// 2. Commits, then every file.
	commitTitle := "COMMITS (" + group3(int64(len(commits))) + "):"
	anyCommit := false
	for i, g := range commits {
		cost := len(commitLine(g, ref)) + 1
		if !anyCommit {
			cost += len(commitTitle) + 1
		}
		if take(cost) {
			commitShown[i], anyCommit = true, true
		}
	}
	fileTitle := "FILES (" + group3(int64(len(files))) + "):"
	anyFile := false
	for i, f := range files {
		cost := len(fileLine(f, ref)) + 1
		if !anyFile {
			cost += len(fileTitle) + 1
		}
		if take(cost) {
			fileShown[i], anyFile = true, true
		} else {
			break
		}
	}
	// 3. Preserved items' lines, in rounds.
	for _, limit := range []int{2, 6, 16, maxShowDetail} {
		for _, s := range secs[:4] {
			for i, g := range s.groups {
				if !s.shown[i] || s.detail[i] >= len(g.ex.Detail) {
					continue
				}
				want := min(limit, len(g.ex.Detail))
				have := detailCost(g, s.detail[i])
				for k := want; k > s.detail[i]; k-- {
					if take(detailCost(g, k) - have) {
						s.detail[i] = k
						break
					}
				}
			}
		}
	}
	// 4. The changes: each hunk with a few of its lines, then more of them
	// in rounds.
	changeTitle := "CHANGES (" + group3(int64(len(hunks))) + "):"
	anyHunk := false
	for i, g := range hunks {
		k := min(3, len(g.ex.Detail))
		cost := len(hunkLine(g, ref)) + 1 + detailCost(g, k)
		if !anyHunk {
			cost += len(changeTitle) + 1
		}
		if take(cost) {
			hunkShown[i], hunkDetail[i], anyHunk = true, k, true
		}
	}
	for _, limit := range []int{10, 30, maxShowDetail} {
		for i, g := range hunks {
			if !hunkShown[i] || hunkDetail[i] >= len(g.ex.Detail) {
				continue
			}
			want := min(limit, len(g.ex.Detail))
			have := detailCost(g, hunkDetail[i])
			for k := want; k > hunkDetail[i]; k-- {
				if take(detailCost(g, k) - have) {
					hunkDetail[i] = k
					break
				}
			}
		}
	}

	var out []string
	out = append(out, head...)
	overflow := false
	for _, s := range secs[:4] {
		if s.any {
			out = append(out, sectionTitle(s, false))
		}
		for i, g := range s.groups {
			if !s.shown[i] {
				overflow = true
				continue
			}
			out = append(out, itemLine(s, g, ref))
			out = append(out, detailLines(g, s.detail[i])...)
		}
	}
	if anyCommit {
		out = append(out, commitTitle)
		for i, g := range commits {
			if commitShown[i] {
				out = append(out, commitLine(g, ref))
			}
		}
	}
	if anyFile {
		out = append(out, fileTitle)
		for i, f := range files {
			if fileShown[i] {
				out = append(out, fileLine(f, ref))
			}
		}
	}
	if anyHunk {
		out = append(out, changeTitle)
		for i, g := range hunks {
			if hunkShown[i] {
				out = append(out, hunkLine(g, ref))
				out = append(out, detailLines(g, hunkDetail[i])...)
			}
		}
	}
	out = append(out, footer(secs, nil, nil, false, ref, false)...)
	out = append(out, extra(false)...)
	text := strings.Join(out, "\n") + "\n"
	if len(text) > budget {
		return "", false, &BudgetError{Budget: budget, Need: len(text)}
	}
	return text, overflow, nil
}

func commitLine(g *group, ref func(string) string) string {
	return "- " + cut(g.ex.Head, maxHead) + ref("g"+strconv.Itoa(g.idx))
}

func fileLine(f diffFile, ref func(string) string) string {
	p := f.path
	if f.status == 'R' || f.status == 'C' {
		p = f.oldPath + " -> " + f.path
	}
	s := "- " + string(f.status) + " " + cut(p, maxHead)
	switch {
	case f.binary:
		s += " (binary)"
	default:
		s += " (+" + group3(f.adds) + " -" + group3(f.dels) + ")"
	}
	if f.generated {
		s += " (generated)"
	}
	sel := "L" + strconv.FormatInt(f.start, 10)
	if f.end > f.start {
		sel += "-" + strconv.FormatInt(f.end, 10)
	}
	return s + ref(sel)
}

func hunkLine(g *group, ref func(string) string) string {
	s := "- "
	if g.count > 1 {
		s += "x" + strconv.FormatInt(g.count, 10) + " "
	}
	s += cut(g.ex.Head, maxHead)
	if g.count > 1 {
		s += " (the same change in " + strconv.FormatInt(g.count, 10) + " places)"
	}
	return s + ref("g"+strconv.Itoa(g.idx))
}
