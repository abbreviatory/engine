package abbreviatory

import (
	"encoding/binary"
	"regexp"
	"strings"
)

// maxDistinct is how many distinct groups of one kind are kept apart. Past
// it, further distinct records of that kind share one group, so memory stays
// bounded on inputs with millions of unique lines. The lines stay referenced.
const maxDistinct = 50000

// maxDetail is how many detail lines an exemplar keeps.
const maxDetail = 200

// group is a set of records with the same fingerprint: one event and its
// repeats.
type group struct {
	idx    int
	kind   Kind
	ex     Record // the first record, shown as the example
	count  int64  // records in the group
	lines  int64  // input lines they cover
	ranges rangeSet
	kept   bool // a keep rule matched
	more   bool // the catch-all group past maxDistinct
	first  int64
}

type grouper struct {
	rules    preserve
	keep     []*regexp.Regexp
	byKey    map[string]*group
	order    []*group
	distinct [6]int
	overflow [6]*group
	records  map[string]int64
	lines    map[string]int64
	bytes    bool // ranges are byte offsets, not lines
}

func newGrouper(rules preserve, keep []*regexp.Regexp) *grouper {
	return &grouper{
		rules:   rules,
		keep:    keep,
		byKey:   map[string]*group{},
		records: map[string]int64{},
		lines:   map[string]int64{},
	}
}

func (g *grouper) matches(r *Record) bool {
	for _, re := range g.keep {
		if re.MatchString(r.Head) {
			return true
		}
		for _, d := range r.Detail {
			if re.MatchString(d) {
				return true
			}
		}
	}
	return false
}

func (g *grouper) add(r Record) { g.addN(r, 1) }

// addN adds n records the same as r. Records counted from a runner's own
// summary come in this way, with no lines of their own.
func (g *grouper) addN(r Record, n int64) {
	if n <= 0 {
		return
	}
	if strings.TrimSpace(r.Head) == "" {
		r.Head = "(blank line)"
	}
	// Keep rules match lines of the input; a record with no lines has none.
	kept := len(g.keep) > 0 && len(r.Lines) > 0 && g.matches(&r)
	if r.Kind == KindPass && !kept {
		// A passing test's output only matters to keep rules.
		r.Detail = nil
	}
	key := fingerprint(&r)
	if kept {
		key += "\x00k"
	}
	gr := g.byKey[key]
	if gr == nil {
		if g.distinct[r.Kind] >= maxDistinct && !kept {
			gr = g.overflow[r.Kind]
			if gr == nil {
				gr = &group{kind: r.Kind, more: true, first: firstLine(r.Lines)}
				gr.ex = Record{Kind: r.Kind, Head: "(further distinct " + r.Kind.String() + " lines, past " + group3(maxDistinct) + " kinds)"}
				g.overflow[r.Kind] = gr
				g.order = append(g.order, gr)
			}
		} else {
			if len(r.Detail) > maxDetail {
				r.Detail = append(r.Detail[:maxDetail:maxDetail], "... ("+group3(int64(len(r.Detail)-maxDetail))+" more lines)")
			}
			gr = &group{kind: r.Kind, ex: r, kept: kept, first: firstLine(r.Lines)}
			gr.ex.Lines = nil
			g.byKey[key] = gr
			g.order = append(g.order, gr)
			g.distinct[r.Kind]++
		}
	}
	gr.count += n
	var lines int64
	for _, rg := range mergeRanges(r.Lines) {
		gr.ranges.add(rg)
		lines += rg.End - rg.Start + 1
	}
	if g.bytes {
		lines = 0
	}
	gr.lines += lines
	g.records[r.Kind.String()] += n
	g.lines[r.Kind.String()] += lines
}

func (g *grouper) finish() []*group {
	for i, gr := range g.order {
		gr.idx = i
	}
	return g.order
}

func firstLine(rs []Range) int64 {
	if len(rs) == 0 {
		return 0
	}
	m := rs[0].Start
	for _, r := range rs[1:] {
		if r.Start < m {
			m = r.Start
		}
	}
	return m
}

// rangeSet holds line ranges compactly: each range as two varints (gap from
// the previous range's end, and length), with the last range kept open so
// touching lines extend it.
type rangeSet struct {
	buf     []byte
	last    Range
	has     bool
	prevEnd int64
	ordered bool // false once a range arrives out of order
	extra   []Range
}

func (s *rangeSet) add(r Range) {
	if !s.has {
		s.last, s.has, s.ordered = r, true, true
		return
	}
	if r.Start >= s.last.Start && r.Start <= s.last.End+1 {
		if r.End > s.last.End {
			s.last.End = r.End
		}
		return
	}
	if r.Start <= s.last.End {
		// Out of order (interleaved test output). Rare; keep these aside.
		s.extra = append(s.extra, r)
		return
	}
	s.flush()
	s.last = r
}

func (s *rangeSet) flush() {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(s.last.Start-s.prevEnd))
	s.buf = append(s.buf, tmp[:n]...)
	n = binary.PutUvarint(tmp[:], uint64(s.last.End-s.last.Start))
	s.buf = append(s.buf, tmp[:n]...)
	s.prevEnd = s.last.End
}

// all returns the ranges sorted and merged.
func (s *rangeSet) all() []Range {
	if !s.has {
		return nil
	}
	var out []Range
	var prev int64
	for i := 0; i < len(s.buf); {
		gap, n := binary.Uvarint(s.buf[i:])
		i += n
		ln, m := binary.Uvarint(s.buf[i:])
		i += m
		start := prev + int64(gap)
		out = append(out, Range{start, start + int64(ln)})
		prev = start + int64(ln)
	}
	out = append(out, s.last)
	if len(s.extra) > 0 {
		out = mergeRanges(append(out, s.extra...))
	}
	return out
}

func buildIndex(res *Result, groups []*group) *Index {
	unit := "lines"
	if res.Format == "junit" {
		unit = "bytes"
	}
	ix := &Index{ID: res.ID, Format: res.Format, Lines: res.InputLines, Unit: unit, Groups: make([]IndexGroup, len(groups))}
	for i, gr := range groups {
		rs := gr.ranges.all()
		pairs := make([][2]int64, len(rs))
		for j, r := range rs {
			pairs[j] = [2]int64{r.Start, r.End}
		}
		ix.Groups[i] = IndexGroup{Kind: gr.kind.String(), Kept: gr.kept, Ranges: pairs}
	}
	return ix
}
