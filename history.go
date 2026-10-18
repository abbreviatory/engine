package abbreviatory

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
)

// MaxGood is how many passing tests a run keeps in full. A bigger suite
// keeps only those that failed in one of the runs kept, which is what
// telling fixed from not run needs; flaky detection there counts only the
// changes after a test's first failure in the window.
const MaxGood = 100000

// FlakyWindow is how many runs, the current one included, are looked at to
// decide whether an item is flaky, and FlakyFlips how many times its status
// must change in them.
const (
	FlakyWindow = 10
	FlakyFlips  = 3
)

// Run is what history keeps of one reduction: enough to compare the next run
// with it. Bad and Good are only needed for the last FlakyWindow runs; older
// runs can keep just the counts.
type Run struct {
	N       int              `json:"n"`
	Time    string           `json:"time,omitempty"` // set by the caller; Reduce doesn't read the clock
	ID      string           `json:"ref"`
	Format  string           `json:"format"`
	Command string           `json:"command,omitempty"`
	Exit    *int             `json:"exit,omitempty"`
	Records map[string]int64 `json:"records,omitempty"`
	// Failing is how many distinct failures and errors the run had, and New
	// and Fixed how many changed since the run before.
	Failing int `json:"failing"`
	New     int `json:"new,omitempty"`
	Fixed   int `json:"fixed,omitempty"`
	// Bad holds the failures and errors, keyed by a hash of their identity.
	Bad map[string]RunItem `json:"bad,omitempty"`
	// Good holds the hashes of the tests that passed, packed and sorted.
	Good string `json:"good,omitempty"`
	// Anon is true when the output named no passing test (go test without
	// -v, pytest without -v): a test missing from it most likely passed.
	Anon bool `json:"anon,omitempty"`
	// Ran lists, for such output from go test, the packages whose tests
	// ran: a missing test of a package that failed to build didn't pass.
	Ran []string `json:"ran,omitempty"`
	// Full is false once Bad and Good have been dropped from an old run.
	Full bool `json:"full,omitempty"`
}

// RunItem is one failure or error of a run.
type RunItem struct {
	Head string `json:"head"`
	// Test is true when the identity is a test name: a test that doesn't
	// appear in a run wasn't run, which says nothing about it. Anything else
	// (a log error, a build failure) that doesn't appear didn't happen.
	Test bool `json:"test,omitempty"`
	// Streak is how many runs in a row, this one included, it was failing.
	Streak int `json:"streak"`
	// Sel is the reference selector that shows it in this run's original.
	Sel string `json:"sel,omitempty"`
	// Name is a go test's name without its package; Bare is true when the
	// run didn't print the package (it was cut short), so the item is
	// matched by Name with the same test in the other run.
	Name string `json:"name,omitempty"`
	Bare bool   `json:"bare,omitempty"`
	Pkg  string `json:"pkg,omitempty"` // a go test's package
}

// Trim drops what only the last FlakyWindow runs need from a run older than
// that.
func (r *Run) Trim() {
	r.Bad, r.Good, r.Full = nil, "", false
}

// Delta is what changed since the previous run.
type Delta struct {
	Run      int // this run's number
	Since    int // the run compared with; 0 when there is none
	Exit     *int
	PrevExit *int // the previous run's exit status, when it differs
	Tests    bool // test output: the words are "fixed", "flaky"; else "gone", "intermittent"
	// Identical is true when the input is byte for byte the previous one.
	// SameFailures is true when the same failures and errors are present,
	// and Unchanged when also the exit status is the same: the report is
	// then one line.
	Identical    bool
	SameFailures bool
	Unchanged    bool
	Failing      int
	New          []DeltaItem
	Fixed        []DeltaItem // failing before, passing (or for logs, absent) now
	NotRun       []DeltaItem // a failing test that doesn't appear in this run
	Flaky        []DeltaItem
	Still        int // failing before and now, not flaky
	Longest      int // the longest streak among them
}

// DeltaItem is one line of a delta.
type DeltaItem struct {
	Key    string
	Head   string
	Ref    string // a reference selector with its ID, such as 3f2a9c1b7e04:g12
	Now    string // for flaky items: "failing", "passing" or "not run"
	Flips  int
	Window int
	Streak int
}

// item is a failure, an error or a passing test of the current run.
type item struct {
	key  string // hex hash
	test bool
	head string
	sel  string
	name string // a go test's own name
	bare bool   // its package wasn't printed
	pkg  string // its package
}

func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// runItems lists the failures and errors of a reduction, in the order they
// appear, and the tests that passed.
func runItems(res *Result, groups []*group, passIDs map[string]bool, doc *jsonDoc, rules preserve) (bad []item, good map[string]bool) {
	seen := map[string]bool{}
	good = map[string]bool{}
	tests := res.Family == FamilyTests
	if doc != nil {
		if !rules.errors {
			return nil, good
		}
		for _, p := range doc.errors {
			n, err := doc.lookup(p)
			if err != nil {
				continue
			}
			v := strings.Join(strings.Fields(string(doc.data[n.start:n.end])), " ")
			k := hashKey("e\x00j\x00" + normalize(p) + "\x00" + normalize(cut(v, maxHead)))
			if seen[k] {
				continue
			}
			seen[k] = true
			bad = append(bad, item{key: k, head: p + ": " + v, sel: refPointer(p)})
		}
		return bad, good
	}
	for _, g := range groups {
		if g.more {
			continue
		}
		if (g.kind == KindFail && rules.failures) || (g.kind == KindError && rules.errors) {
			k, test := groupKey(g, tests)
			if seen[k] {
				continue
			}
			seen[k] = true
			it := item{key: k, test: test, head: g.ex.Head, sel: "g" + strconv.Itoa(g.idx)}
			if test && g.ex.Name != "" {
				it.name, it.bare = g.ex.Name, g.ex.Name == g.ex.ID
				if !it.bare && strings.HasSuffix(g.ex.ID, "."+g.ex.Name) {
					it.pkg = strings.TrimSuffix(g.ex.ID, "."+g.ex.Name)
				}
			}
			bad = append(bad, it)
		}
	}
	// Every passing test, past the cap on distinct groups too.
	for k := range passIDs {
		if !seen[k] {
			good[k] = true
		}
	}
	return bad, good
}

// groupKey is a failure's identity across runs. A failed test is its test
// name. Anything else (a log error, a build or package failure, a crash) is
// what it says, numbers and IDs aside, or its runner's name for it.
func groupKey(g *group, tests bool) (string, bool) {
	if tests && g.ex.ID != "" {
		if g.kind == KindFail {
			return hashKey("t\x00" + g.ex.ID), true
		}
		return hashKey("e\x00id\x00" + g.ex.ID), false
	}
	return hashKey("e\x00" + g.fp), false
}

func packGood(good map[string]bool) string {
	keys := make([]string, 0, len(good))
	for k := range good {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	buf := make([]byte, 0, 8*len(keys))
	for _, k := range keys {
		b, err := hex.DecodeString(k)
		if err != nil || len(b) != 8 {
			continue
		}
		buf = append(buf, b...)
	}
	return base64.StdEncoding.EncodeToString(buf)
}

func unpackGood(s string) map[string]bool {
	out := map[string]bool{}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return out
	}
	for i := 0; i+8 <= len(b); i += 8 {
		out[hex.EncodeToString(b[i:i+8])] = true
	}
	return out
}

// statusIn is an item's status in a run: 'F' failing, 'P' passing (or, for
// what isn't a test, absent), '-' unknown.
func statusIn(r *Run, good map[string]bool, key string, test bool, pkg string) byte {
	if _, ok := r.Bad[key]; ok {
		return 'F'
	}
	if !test || good[key] {
		return 'P'
	}
	if r.Anon {
		// Passing tests went unnamed; a missing test passed if its package
		// ran (when the output says which did).
		if len(r.Ran) == 0 || pkg == "" {
			return 'P'
		}
		for _, p := range r.Ran {
			if p == pkg {
				return 'P'
			}
		}
	}
	return '-'
}

// flips counts the changes between known statuses.
func flips(st []byte) int {
	n := 0
	var last byte
	for _, s := range st {
		if s == '-' {
			continue
		}
		if last != 0 && s != last {
			n++
		}
		last = s
	}
	return n
}

// computeDelta compares the current run with history (oldest first) and
// fills in cur's counts and streaks.
func computeDelta(history []Run, cur *Run, bad []item, good, passNames map[string]bool, tests bool) *Delta {
	d := &Delta{Run: cur.N, Exit: cur.Exit, Tests: tests, Failing: len(bad)}
	cur.Failing = len(bad)
	var prev *Run
	if len(history) > 0 {
		prev = &history[len(history)-1]
		d.Since = prev.N
	}
	if prev == nil || !prev.Full {
		// Nothing to compare with: every failure starts a streak.
		for _, it := range bad {
			cur.Bad[it.key] = RunItem{Head: it.head, Test: it.test, Streak: 1, Sel: it.sel, Name: it.name, Bare: it.bare, Pkg: it.pkg}
		}
		if prev != nil {
			d.Since = 0
		}
		if tests {
			cur.Good = packGood(keepGood(good, nil))
		}
		return d
	}
	prevBad := matchBare(prev.Bad, bad)
	d.Identical = prev.ID == cur.ID

	// The window: the full runs among the last FlakyWindow-1, then this one.
	var win []*Run
	for i := len(history) - 1; i >= 0 && len(win) < FlakyWindow-1; i-- {
		if !history[i].Full {
			break
		}
		win = append(win, &history[i])
	}
	for i, j := 0, len(win)-1; i < j; i, j = i+1, j-1 {
		win[i], win[j] = win[j], win[i]
	}
	if len(win) > 0 {
		// The previous run as matched with this one.
		p := *win[len(win)-1]
		p.Bad = prevBad
		win[len(win)-1] = &p
	}
	goods := make([]map[string]bool, len(win))
	for i, r := range win {
		goods[i] = unpackGood(r.Good)
	}
	window := len(win) + 1
	statuses := func(key string, test bool, pkg string, now byte) []byte {
		st := make([]byte, 0, window)
		for i, r := range win {
			st = append(st, statusIn(r, goods[i], key, test, pkg))
		}
		return append(st, now)
	}
	ref := func(id, sel string) string {
		if sel == "" {
			return id
		}
		return id + ":" + sel
	}

	curSet := map[string]bool{}
	for _, it := range bad {
		curSet[it.key] = true
		streak := 1
		p, wasBad := prevBad[it.key]
		if wasBad {
			streak = p.Streak + 1
		}
		cur.Bad[it.key] = RunItem{Head: it.head, Test: it.test, Streak: streak, Sel: it.sel, Name: it.name, Bare: it.bare, Pkg: it.pkg}
		di := DeltaItem{Key: it.key, Head: it.head, Ref: ref(cur.ID, it.sel), Streak: streak, Window: window}
		if f := flips(statuses(it.key, it.test, it.pkg, 'F')); f >= FlakyFlips {
			di.Flips, di.Now = f, "failing"
			d.Flaky = append(d.Flaky, di)
			continue
		}
		if wasBad {
			d.Still++
			if streak > d.Longest {
				d.Longest = streak
			}
			continue
		}
		d.New = append(d.New, di)
	}
	// What was failing before and isn't now, in the order of the previous
	// run's references.
	type gone struct {
		key string
		it  RunItem
	}
	var was []gone
	for k, it := range prevBad {
		if !curSet[k] {
			was = append(was, gone{k, it})
		}
	}
	sort.Slice(was, func(i, j int) bool {
		a, b := selOrder(was[i].it.Sel), selOrder(was[j].it.Sel)
		if a != b {
			return a < b
		}
		if was[i].it.Head != was[j].it.Head {
			return was[i].it.Head < was[j].it.Head
		}
		return was[i].key < was[j].key
	})
	for _, w := range was {
		now := statusIn(cur, good, w.key, w.it.Test, w.it.Pkg)
		if now == '-' && w.it.Bare && passNames[w.it.Name] {
			// Failing in a run cut short before its package was printed,
			// passing now under its package.
			now = 'P'
		}
		di := DeltaItem{Key: w.key, Head: w.it.Head, Ref: ref(prev.ID, w.it.Sel), Window: window}
		if f := flips(statuses(w.key, w.it.Test, w.it.Pkg, now)); f >= FlakyFlips {
			di.Flips, di.Now = f, "passing"
			if now == '-' {
				di.Now = "not run"
			}
			d.Flaky = append(d.Flaky, di)
			continue
		}
		if now == 'P' {
			d.Fixed = append(d.Fixed, di)
		} else {
			d.NotRun = append(d.NotRun, di)
		}
	}
	d.SameFailures = len(d.New) == 0 && len(d.Fixed) == 0 && len(d.NotRun) == 0 && len(curSet) == len(prevBad)
	if d.SameFailures {
		// Flaky items that are failing as they were before change nothing.
		for _, f := range d.Flaky {
			if f.Now != "failing" {
				d.SameFailures = false
			}
		}
	}
	d.Unchanged = d.SameFailures
	if !sameExit(prev.Exit, cur.Exit) {
		d.PrevExit = prev.Exit
		d.Unchanged = false
	}
	cur.New = len(d.New)
	cur.Fixed = len(d.Fixed)
	if tests {
		cur.Good = packGood(keepGood(good, win))
	}
	return d
}

// keepGood is the passing tests a run keeps: all of them, or past MaxGood
// those that failed in one of the window's runs.
func keepGood(good map[string]bool, win []*Run) map[string]bool {
	if len(good) <= MaxGood {
		return good
	}
	out := map[string]bool{}
	for _, r := range win {
		for k := range r.Bad {
			if good[k] {
				out[k] = true
			}
		}
	}
	return out
}

// matchBare pairs go tests printed without their package (a run cut short
// before the package's result line) with the same test, by name, in the
// other run. Current items take the previous run's key; previous items take
// the current key. It returns the previous run's failures as matched.
func matchBare(prevBad map[string]RunItem, bad []item) map[string]RunItem {
	prevByName := map[string][]string{}
	for k, it := range prevBad {
		if it.Name != "" {
			prevByName[it.Name] = append(prevByName[it.Name], k)
		}
	}
	taken := map[string]bool{}
	for _, it := range bad {
		taken[it.key] = true
	}
	for i, it := range bad {
		if !it.bare {
			continue
		}
		// With the name in several packages, take the first free one in a
		// fixed order: some pairing beats calling it new and the others
		// not run.
		ks := prevByName[it.name]
		sort.Strings(ks)
		for _, k := range ks {
			if !taken[k] {
				delete(taken, it.key)
				bad[i].key = k
				bad[i].pkg = prevBad[k].Pkg
				taken[k] = true
				break
			}
		}
	}
	curByName := map[string][]string{}
	for _, it := range bad {
		if it.name != "" {
			curByName[it.name] = append(curByName[it.name], it.key)
		}
	}
	out := prevBad
	copied := false
	for k, it := range prevBad {
		if !it.Bare {
			continue
		}
		ks := curByName[it.Name]
		sort.Strings(ks)
		target := ""
		for _, c := range ks {
			if c == k {
				target = ""
				break
			}
			if _, clash := out[c]; !clash && target == "" {
				target = c
			}
		}
		if target == "" {
			continue
		}
		if !copied {
			// Don't change the history's own map.
			out = make(map[string]RunItem, len(prevBad))
			for kk, vv := range prevBad {
				out[kk] = vv
			}
			copied = true
		}
		delete(out, k)
		out[target] = it
	}
	return out
}

// sameExit compares exit statuses; an unknown one (a reduce of piped
// output) matches anything.
func sameExit(a, b *int) bool {
	return a == nil || b == nil || *a == *b
}

// selOrder sorts group selectors by number, others after them.
func selOrder(sel string) int {
	if strings.HasPrefix(sel, "g") {
		if n, err := strconv.Atoi(sel[1:]); err == nil {
			return n
		}
	}
	return 1 << 30
}

// noteGroups marks the groups of the current run: new, flaky or failing for
// some runs, and sorts new ones first in their sections.
func noteGroups(d *Delta, cur *Run, groups []*group, tests bool) {
	class := map[string]int{}
	note := map[string]string{}
	for _, it := range d.New {
		class[it.Key], note[it.Key] = 0, "new"
	}
	for _, it := range d.Flaky {
		if it.Now == "failing" {
			class[it.Key] = 1
			if tests {
				note[it.Key] = "flaky"
			} else {
				note[it.Key] = "intermittent"
			}
		}
	}
	for _, g := range groups {
		if g.more || (g.kind != KindFail && g.kind != KindError) {
			continue
		}
		k, _ := groupKey(g, tests)
		if c, ok := class[k]; ok {
			g.rank, g.note = c, note[k]
			continue
		}
		g.rank = 2
		if d.Since > 0 {
			if s := cur.Bad[k].Streak; s > 1 {
				if tests {
					g.note = "failing for " + strconv.Itoa(s) + " runs"
				} else {
					g.note = "for " + strconv.Itoa(s) + " runs"
				}
			}
		}
	}
}

// blockLines renders the delta as lines placed under the report's header.
// At most cap items of each kind are listed.
func blockLines(d *Delta, capN int, refs bool) []string {
	words := struct{ fixed, flaky, still string }{"fixed", "flaky", "still failing"}
	if !d.Tests {
		words.fixed, words.flaky, words.still = "gone", "intermittent", "recurring"
	}
	first := runLabel(d) + "; "
	if d.Since == 0 {
		first += "no earlier runs to compare with"
		if d.Failing > 0 {
			first += " (" + plural(int64(d.Failing), "failure", "failures") + " recorded)"
		}
		return []string{first}
	}
	var parts []string
	add := func(n int, w string) {
		if n > 0 {
			parts = append(parts, group3(int64(n))+" "+w)
		}
	}
	add(len(d.New), "new")
	add(len(d.Fixed), words.fixed)
	add(len(d.NotRun), "not run")
	if d.Still > 0 {
		s := group3(int64(d.Still)) + " " + words.still
		if d.Longest > 1 {
			s += " (longest for " + strconv.Itoa(d.Longest) + " runs)"
		}
		parts = append(parts, s)
	}
	add(len(d.Flaky), words.flaky)
	since := "since run " + strconv.Itoa(d.Since)
	if d.Identical {
		since += " (identical input)"
	}
	switch {
	case len(parts) == 0 && d.Unchanged:
		first += since + ": no change, no failures"
	case len(parts) == 0:
		first += since + ": no failures, as before"
	case d.Unchanged:
		first += since + ": no change; " + strings.Join(parts, ", ")
	case d.SameFailures:
		first += since + ": the same failures; " + strings.Join(parts, ", ")
	default:
		first += since + ": " + strings.Join(parts, ", ")
	}
	out := []string{first}
	list := func(word string, items []DeltaItem, noun string, extra func(DeltaItem) string) {
		for i, it := range items {
			if i == capN {
				out = append(out, "... and "+group3(int64(len(items)-capN))+" more "+noun)
				break
			}
			s := word + ": " + cut(it.Head, 160)
			if extra != nil {
				s += extra(it)
			}
			if refs {
				s += " [ref " + it.Ref + "]"
			}
			out = append(out, s)
		}
	}
	newNoun := "new failures"
	if !d.Tests {
		newNoun = "new errors"
	}
	list("new", d.New, newNoun, nil)
	list(words.fixed, d.Fixed, words.fixed, nil)
	list("not run", d.NotRun, "not run", nil)
	list(words.flaky, d.Flaky, words.flaky, func(it DeltaItem) string {
		return " (" + it.Now + " now, " + strconv.Itoa(it.Flips) + " changes in the last " + strconv.Itoa(it.Window) + " runs)"
	})
	return out
}

func runLabel(d *Delta) string {
	s := "run " + strconv.Itoa(d.Run)
	if d.Exit != nil {
		s += ", exit status " + strconv.Itoa(*d.Exit)
		if d.PrevExit != nil {
			s += " (was " + strconv.Itoa(*d.PrevExit) + ")"
		}
	}
	return s
}

// unchangedLine is the whole report when nothing changed since the last
// run: one line.
func unchangedLine(res *Result, d *Delta, refs bool) string {
	s := headerLine(res, refs) + "; " + runLabel(d) + ", "
	if d.Identical {
		s += "identical to run " + strconv.Itoa(d.Since)
	} else {
		s += "no change since run " + strconv.Itoa(d.Since)
	}
	noun := "failures"
	if !d.Tests {
		noun = "errors"
	}
	if d.Failing == 0 {
		return s + ": no " + noun + "\n"
	}
	s += ": the same " + group3(int64(d.Failing)) + " "
	if d.Failing == 1 {
		noun = strings.TrimSuffix(noun, "s")
	}
	return s + noun + "\n"
}
