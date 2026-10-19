package abbreviatory

import (
	"bytes"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---- directory listings: find, fd, ls -l, ls -R, tree, du ----

type listEntry struct {
	path  string
	size  int64 // -1 when the listing doesn't say
	isDir bool
}

type listingAdapter struct {
	dir      string   // the directory an "ls -R" section is about
	tree     []string // the directories above the current tree line
	pending  *listEntry
	pendLine int64
	pendLvl  int

	files, dirs int64
	size        int64
	sized       bool
	dirSet      map[string]bool
	exts        map[string]int64
	largest     []listEntry
	du          bool  // du rows: directory sizes include what's in them
	duMax       int64 // the largest du row, usually the total
	treeDirs    int64 // tree's own count, when it printed one
	treeFiles   int64
	treeCount   bool
}

func newListing() *listingAdapter {
	return &listingAdapter{dirSet: map[string]bool{}, exts: map[string]int64{}}
}

var (
	lsRow   = regexp.MustCompile(`^([-dlcbpsD])[-rwxsStTlL]{9}[.+@]?\s+\d+\s+\S+\s+\S+\s+(\d[\d,]*|\d+,\s*\d+)\s+(?:[A-Z][a-z]{2}\s+\d{1,2}\s+(?:\d{1,2}:\d{2}|\d{4})|\d{4}-\d{2}-\d{2}(?:\s+\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:\s+[+-]\d{4})?)?)\s+(.+)$`)
	duRow   = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)([KMGTPE]i?B?)?\t(.+)$`)
	treeRow = regexp.MustCompile(`^((?:[│|][ \x{00a0}]{3}|[ \x{00a0}]{4})*)(?:├──|└──|\|--|` + "`" + `--)[ \x{00a0}](.+)$`)
	listErr = regexp.MustCompile(`(?i)(permission denied|no such file or directory|not a directory|operation not permitted|input/output error|too many levels of symbolic links)`)
)

const maxDirSet = 200000

func (a *listingAdapter) line(n int64, text string, emit func(Record)) {
	t := strings.TrimRight(text, " \t")
	if m := treeRow.FindStringSubmatch(t); m != nil {
		level := len([]rune(m[1])) / 4
		a.treeEntry(n, level, m[2], emit)
		return
	}
	a.flushTree(emit)
	switch {
	case t == "" || strings.HasPrefix(t, "total ") && isDigits(strings.TrimPrefix(t, "total ")):
		a.meta(n, text, emit)
		return
	case listErr.MatchString(t):
		emit(Record{Kind: KindError, Head: t, Lines: []Range{{n, n}}})
		return
	case strings.HasSuffix(t, ":") && !strings.HasPrefix(t, " ") && !strings.Contains(t, ": "):
		// An "ls -R" section: the entries below are in this directory.
		a.dir = strings.TrimSuffix(strings.TrimSuffix(t, ":"), "/")
		if a.dir == "." {
			a.dir = ""
		}
		a.noteDir(a.dir)
		a.meta(n, text, emit)
		return
	case t == "." || t == "./":
		a.meta(n, text, emit)
		return
	case treeSummary(t):
		if m := treeCountRe.FindStringSubmatch(t); m != nil {
			// tree knows which entries are directories; its count wins.
			d, _ := strconv.ParseInt(m[1], 10, 64)
			f, _ := strconv.ParseInt(m[2], 10, 64)
			a.treeDirs += d
			a.treeFiles += f
			a.treeCount = true
		}
		a.meta(n, text, emit)
		return
	}
	if m := lsRow.FindStringSubmatch(t); m != nil {
		name := m[3]
		if m[1] == "l" {
			if i := strings.Index(name, " -> "); i >= 0 {
				name = name[:i]
			}
		}
		if name == "." || name == ".." {
			a.meta(n, text, emit)
			return
		}
		size := int64(-1)
		if !strings.Contains(m[2], ",") || !strings.Contains(m[2], " ") {
			size, _ = strconv.ParseInt(strings.ReplaceAll(m[2], ",", ""), 10, 64)
		}
		a.entry(n, listEntry{path: joinPath(a.dir, name), size: size, isDir: m[1] == "d"}, emit)
		return
	}
	if m := duRow.FindStringSubmatch(t); m != nil {
		// du prints a directory after what's in it. Without -a it prints
		// only directories; with -a, files too.
		p := cleanListPath(m[3])
		size := duSize(m[1], m[2])
		a.du = true
		a.duMax = max(a.duMax, size)
		isDir := a.dirSet[p] || path.Ext(p) == ""
		a.entry(n, listEntry{path: p, size: size, isDir: isDir}, emit)
		return
	}
	if pathLike(t) {
		a.entry(n, listEntry{path: cleanListPath(t), size: -1, isDir: strings.HasSuffix(t, "/")}, emit)
		return
	}
	if len(a.tree) == 0 && n == 1 {
		a.meta(n, text, emit) // the root line of a tree
		return
	}
	emit(Record{Kind: levelOf(text), Head: t, Lines: []Range{{n, n}}})
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var (
	treeSumRe   = regexp.MustCompile(`^\d+ director(y|ies)(, \d+ files?)?$`)
	treeCountRe = regexp.MustCompile(`^(\d+) director(?:y|ies)(?:, (\d+) files?)?$`)
)

func treeSummary(t string) bool { return treeSumRe.MatchString(t) }

// pathLike reports a line that is a path, as find and fd print them.
func pathLike(t string) bool {
	if t == "" || len(t) > 4096 || t[0] == ' ' || strings.ContainsAny(t, "\t*?\"<>|") || strings.Contains(t, "://") {
		return false
	}
	if strings.Contains(t, ": ") || strings.Contains(t, "::") {
		return false
	}
	if strings.Contains(t, "==") || t[0] == '#' {
		return false // pip freeze, comments
	}
	if strings.Contains(t, "/") {
		return true
	}
	if strings.ContainsAny(t, "=@#") {
		return false // version pins
	}
	// A bare name needs a plausible extension: a few characters with a
	// letter, not a version number.
	i := strings.LastIndexByte(t, '.')
	if i <= 0 || i == len(t)-1 || strings.Contains(t, " ") {
		return false
	}
	ext := t[i+1:]
	return len(ext) <= 10 && strings.IndexFunc(ext, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) >= 0
}

func cleanListPath(p string) string {
	p = strings.TrimPrefix(p, "./")
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func duSize(num, unit string) int64 {
	f, err := strconv.ParseFloat(strings.ReplaceAll(num, ",", "."), 64)
	if err != nil {
		return -1
	}
	mult := 1024.0 // du's plain numbers are kilobytes
	switch strings.TrimSuffix(strings.TrimSuffix(unit, "B"), "i") {
	case "K":
		mult = 1 << 10
	case "M":
		mult = 1 << 20
	case "G":
		mult = 1 << 30
	case "T":
		mult = 1 << 40
	case "P":
		mult = 1 << 50
	case "E":
		mult = 1 << 60
	}
	return int64(f * mult)
}

// treeEntry handles a tree line. Whether it's a directory shows only on the
// next line (deeper means it was), so one entry waits.
func (a *listingAdapter) treeEntry(n int64, level int, name string, emit func(Record)) {
	if a.pending != nil && level > a.pendLvl {
		a.pending.isDir = true
	}
	a.flushTree(emit)
	if level < len(a.tree) {
		a.tree = a.tree[:level]
	}
	for len(a.tree) < level {
		a.tree = append(a.tree, "?")
	}
	if i := strings.Index(name, " -> "); i >= 0 {
		name = name[:i]
	}
	p := strings.Join(append(append([]string{}, a.tree...), name), "/")
	a.pending = &listEntry{path: p, size: -1, isDir: strings.HasSuffix(name, "/")}
	a.pendLine, a.pendLvl = n, level
	a.tree = append(a.tree, strings.TrimSuffix(name, "/"))
}

func (a *listingAdapter) flushTree(emit func(Record)) {
	if a.pending == nil {
		return
	}
	e := *a.pending
	a.pending = nil
	a.entry(a.pendLine, e, emit)
}

func (a *listingAdapter) meta(n int64, text string, emit func(Record)) {
	emit(Record{Kind: KindInfo, Head: strings.TrimRight(text, " \t"), GroupKey: "meta", GroupHead: "(headings, totals and blank lines)", Lines: []Range{{n, n}}})
}

func (a *listingAdapter) noteDir(d string) {
	if d != "" && len(a.dirSet) < maxDirSet && !a.dirSet[d] {
		a.dirSet[d] = true
	}
}

func (a *listingAdapter) entry(n int64, e listEntry, emit func(Record)) {
	dir := path.Dir(e.path)
	if dir == "." {
		dir = ""
	}
	head := e.path
	if e.isDir {
		a.dirs++
		a.noteDir(e.path)
		head += "/"
		if e.size >= 0 {
			head += " (" + humanBytes(e.size) + ")"
		}
		emit(Record{Kind: KindInfo, Head: head, GroupKey: "d\x00" + dir, GroupHead: joinPath(dir, "*/") + " (directories)", Lines: []Range{{n, n}}})
		return
	}
	a.files++
	a.noteDir(dir)
	ext := strings.ToLower(path.Ext(e.path))
	if len(ext) > 12 || strings.ContainsAny(ext, " ") {
		ext = ""
	}
	a.exts[ext]++
	if e.size >= 0 {
		a.sized = true
		a.size += e.size
		head += " (" + humanBytes(e.size) + ")"
		a.noteLargest(e)
	}
	glob := "*" + ext
	if ext == "" {
		glob = "* (no extension)"
	}
	emit(Record{Kind: KindInfo, Head: head, GroupKey: "f\x00" + dir + "\x00" + ext, GroupHead: joinPath(dir, glob), Lines: []Range{{n, n}}})
}

func (a *listingAdapter) noteLargest(e listEntry) {
	a.largest = append(a.largest, e)
	sort.SliceStable(a.largest, func(i, j int) bool {
		if a.largest[i].size != a.largest[j].size {
			return a.largest[i].size > a.largest[j].size
		}
		return a.largest[i].path < a.largest[j].path
	})
	if len(a.largest) > 3 {
		a.largest = a.largest[:3]
	}
}

func (a *listingAdapter) finish(emit func(Record)) []string {
	a.flushTree(emit)
	dirs := int64(len(a.dirSet))
	if a.dirs > dirs {
		dirs = a.dirs
	}
	if a.treeCount {
		dirs, a.files = a.treeDirs, a.treeFiles
	}
	s := "listing: " + plural(a.files, "file", "files") + " in " + plural(dirs, "directory", "directories")
	if len(a.dirSet) >= maxDirSet {
		s = "listing: " + plural(a.files, "file", "files") + " in more than " + plural(dirs, "directory", "directories")
	}
	if a.du && a.duMax > a.size {
		a.size, a.sized = a.duMax, true
	}
	if a.sized {
		s += ", " + humanBytes(a.size)
	}
	type ec struct {
		ext string
		n   int64
	}
	var es []ec
	for e, c := range a.exts {
		es = append(es, ec{e, c})
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].n != es[j].n {
			return es[i].n > es[j].n
		}
		return es[i].ext < es[j].ext
	})
	var top []string
	for i, e := range es {
		if i == 4 {
			break
		}
		name := e.ext
		if name == "" {
			name = "no extension"
		}
		top = append(top, name+" "+group3(e.n))
	}
	if len(top) > 0 {
		s += "; most: " + strings.Join(top, ", ")
	}
	out := []string{s}
	if len(a.largest) > 0 {
		var l []string
		for _, e := range a.largest {
			l = append(l, cut(e.path, 80)+" "+humanBytes(e.size))
		}
		out = append(out, "largest: "+strings.Join(l, ", "))
	}
	return out
}

// looksListing reports a directory listing: nearly every line is a path,
// an ls -l row, a tree line, a du line, or one of their headings.
func looksListing(sample []byte) bool {
	lines := bytes.Split(sample, []byte("\n"))
	if len(lines) > 400 {
		lines = lines[:400]
	}
	entries, other := 0, 0
	for i, l := range lines {
		t := strings.TrimRight(string(l), " \t\r")
		switch {
		case t == "":
		case lsRow.MatchString(t), treeRow.MatchString(t), duRow.MatchString(t):
			entries++
		case strings.HasPrefix(t, "total ") && isDigits(strings.TrimPrefix(t, "total ")),
			strings.HasSuffix(t, ":") && !strings.Contains(t, " "),
			treeSummary(t), i == 0 && (t == "." || !strings.Contains(t, " ")):
		case listErr.MatchString(t) && (strings.HasPrefix(t, "ls: ") || strings.HasPrefix(t, "find: ") || strings.HasPrefix(t, "du: ") || strings.HasPrefix(t, "tree: ")):
			// The tool's own complaints don't count either way.
		case pathLike(t) && levelOf(t) == KindInfo:
			entries++
		default:
			other++
		}
	}
	return entries >= 5 && other*10 <= entries
}
