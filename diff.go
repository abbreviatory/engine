package abbreviatory

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ---- unified diffs: git diff, git show, git log -p, diff -u ----

// diffFile is one file of a diff, as the report's file list shows it.
type diffFile struct {
	path, oldPath string
	status        byte // A added, D deleted, M modified, R renamed, C copied
	binary        bool
	generated     bool
	adds, dels    int64
	hunks         int
	start, end    int64 // its lines in the input
}

type diffAdapter struct {
	files   []diffFile
	commits int

	cur      *diffFile
	header   *Record // the file's header lines, until its first hunk
	hunk     *Record // the hunk being read
	hunkKeep []string
	hunkSum  hash.Hash // all of the hunk's changed lines, for its grouping identity
	parents  int       // prefix columns: 1, or one per parent in a combined diff
	ccOld    []int64   // a combined hunk's lines left of each parent
	oldLeft  int64
	newLeft  int64
	added    []string // the hunk's added lines, to check them
	commit   *Record
	pendingP *Record // "--- " seen outside a file: maybe a plain diff -u
}

func newDiff() *diffAdapter { return &diffAdapter{} }

var (
	hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@ ?(.*)$`)
	// A combined diff (diff --cc, a merge): one "-" range per parent.
	ccHeader  = regexp.MustCompile(`^(@{3,}) (?:-\d+(?:,\d+)? )+\+\d+(?:,(\d+))? @{3,}`)
	ccRange   = regexp.MustCompile(` -\d+(?:,(\d+))?`)
	gitDiffRe = regexp.MustCompile(`^diff --git ("?)a/(.*?)("?) ("?)b/(.*?)("?)$`)
	commitRe  = regexp.MustCompile(`^commit ([0-9a-f]{7,64})\b`)
)

// generatedFile reports files a diff reader rarely wants line by line: lock
// files, minified and generated code, vendored dependencies, snapshots.
func generatedFile(p string) bool {
	base := path.Base(p)
	switch base {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock", "poetry.lock",
		"composer.lock", "Gemfile.lock", "Pipfile.lock", "uv.lock", "bun.lockb", "flake.lock", "npm-shrinkwrap.json":
		return true
	}
	for _, suf := range []string{".min.js", ".min.css", ".map", ".pb.go", "_pb2.py", ".pb.gw.go", "_generated.go", ".gen.go", ".snap"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	for _, dir := range []string{"vendor/", "node_modules/", "__snapshots__/", "dist/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	return false
}

func (d *diffAdapter) line(n int64, text string, emit func(Record)) {
	// Inside a hunk, its lines belong to it until its counts run out.
	if d.hunk != nil {
		// A hunk or file header can't be a hunk line (those start with a
		// space, "+", "-" or "\\"): wrong counts end the hunk there.
		next := strings.HasPrefix(text, "diff --git ") || strings.HasPrefix(text, "diff --cc ") || strings.HasPrefix(text, "diff --combined ") ||
			strings.HasPrefix(text, "@@") && (hunkHeader.MatchString(text) || ccHeader.MatchString(text))
		if !next && (d.oldLeft > 0 || d.newLeft > 0 || strings.HasPrefix(text, "\\")) {
			d.hunkLine(n, text)
			return
		}
		d.endHunk(emit)
	}
	switch {
	case strings.HasPrefix(text, "diff --git ") || strings.HasPrefix(text, "diff -"):
		d.endFile(emit)
		d.startFile(n, text)
		return
	case strings.HasPrefix(text, "@@") && d.cur != nil && (hunkHeader.MatchString(text) || ccHeader.MatchString(text)):
		d.startHunk(n, text, emit)
		return
	}
	if d.cur != nil && d.header != nil && d.cur.hunks == 0 && fileHeaderLine(text, d.cur.binary) {
		d.headerLine(n, text)
		return
	}
	if d.pendingP != nil {
		p := d.pendingP
		d.pendingP = nil
		if strings.HasPrefix(text, "+++ ") {
			// A plain unified diff without a "diff" line: the file starts
			// at its "---" line.
			d.endFile(emit)
			d.cur = &diffFile{status: 'M', start: p.Lines[0].Start, end: p.Lines[0].Start}
			d.header = &Record{Kind: KindInfo}
			d.headerLine(p.Lines[0].Start, p.Head)
			d.headerLine(n, text)
			return
		}
		emit(*p)
	}
	if strings.HasPrefix(text, "--- ") && (d.cur == nil || d.cur.hunks > 0) {
		d.pendingP = &Record{Kind: KindInfo, Head: text, Lines: []Range{{n, n}}}
		return
	}
	// Commit headers and messages (git show, git log -p, format-patch).
	if m := commitRe.FindStringSubmatch(text); m != nil || (strings.HasPrefix(text, "From ") && len(text) > 45 && isHexRun(text[5:45])) {
		d.endFile(emit)
		d.endCommit(emit)
		sha := ""
		if m != nil {
			sha = m[1]
		} else {
			sha = text[5:45]
		}
		d.commits++
		d.commit = &Record{Kind: KindInfo, ID: "commit:" + sha, Head: "commit " + sha[:min(len(sha), 12)], Lines: []Range{{n, n}}}
		return
	}
	if d.commit != nil && d.cur == nil {
		d.commit.addLine(n)
		msg := strings.TrimSpace(text)
		if strings.HasPrefix(msg, "Subject: ") {
			msg = strings.TrimPrefix(msg, "Subject: ")
		}
		isHeader := strings.HasPrefix(text, "Author:") || strings.HasPrefix(text, "Date:") || strings.HasPrefix(text, "Merge:") ||
			strings.HasPrefix(text, "From:") || strings.HasPrefix(text, "Commit:") || strings.HasPrefix(text, "AuthorDate:") || strings.HasPrefix(text, "CommitDate:")
		if msg != "" && !isHeader && msg != "---" {
			if !strings.Contains(d.commit.Head, ": ") {
				d.commit.Head += ": " + msg
			} else if len(d.commit.Detail) < maxDetail {
				d.commit.Detail = append(d.commit.Detail, strings.TrimRight(text, " \t"))
			}
		}
		return
	}
	if d.cur != nil {
		// Something between files that isn't a diff line, such as a
		// commit's stat lines or "Only in" from diff -r.
		d.endFile(emit)
	}
	emit(Record{Kind: levelOf(text), Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
}

func isHexRun(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}

func fileHeaderLine(t string, binary bool) bool {
	for _, p := range []string{"old mode ", "new mode ", "deleted file mode ", "new file mode ", "similarity index ", "dissimilarity index ",
		"rename from ", "rename to ", "copy from ", "copy to ", "index ", "--- ", "+++ ", "Binary files ", "GIT binary patch", "literal ", "delta "} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	// The base85 lines of a binary patch, and the blank lines after them.
	return binary && (t == "" || (len(t) > 1 && t[0] >= 'A' && t[0] <= 'z' && !strings.Contains(t, " ")))
}

func (d *diffAdapter) startFile(n int64, text string) {
	d.cur = &diffFile{status: 'M', start: n, end: n}
	if m := gitDiffRe.FindStringSubmatch(text); m != nil {
		d.cur.oldPath = unquoteGitPath(m[2])
		d.cur.path = unquoteGitPath(m[5])
	} else if f := strings.Fields(text); len(f) >= 2 {
		d.cur.path = f[len(f)-1]
	}
	d.header = &Record{Kind: KindInfo, Lines: []Range{{n, n}}}
}

func unquoteGitPath(p string) string {
	if u, err := strconv.Unquote(`"` + p + `"`); err == nil {
		return u
	}
	return p
}

func (d *diffAdapter) headerLine(n int64, text string) {
	d.header.addLine(n)
	f := d.cur
	f.end = n
	switch {
	case strings.HasPrefix(text, "new file mode "):
		f.status = 'A'
	case strings.HasPrefix(text, "deleted file mode "):
		f.status = 'D'
	case strings.HasPrefix(text, "rename from "):
		f.status, f.oldPath = 'R', strings.TrimPrefix(text, "rename from ")
	case strings.HasPrefix(text, "rename to "):
		f.path = strings.TrimPrefix(text, "rename to ")
	case strings.HasPrefix(text, "copy from "):
		f.status, f.oldPath = 'C', strings.TrimPrefix(text, "copy from ")
	case strings.HasPrefix(text, "copy to "):
		f.path = strings.TrimPrefix(text, "copy to ")
	case strings.HasPrefix(text, "Binary files ") || strings.HasPrefix(text, "GIT binary patch"):
		f.binary = true
	case strings.HasPrefix(text, "--- "):
		p := diffPath(strings.TrimPrefix(text, "--- "))
		if p == "/dev/null" {
			f.status = 'A'
		} else if f.path == "" {
			f.path = strings.TrimPrefix(p, "a/")
		}
	case strings.HasPrefix(text, "+++ "):
		p := diffPath(strings.TrimPrefix(text, "+++ "))
		if p == "/dev/null" {
			f.status = 'D'
		} else if d.cur.oldPath == "" || f.path == "" || !strings.HasPrefix(text, "+++ b/") {
			f.path = strings.TrimPrefix(p, "b/")
		}
	}
}

// diffPath takes the path from a "---" or "+++" line, without the time diff
// -u writes after a tab.
func diffPath(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return s
}

// emitHeader emits the file's header record once its status is known.
func (d *diffAdapter) emitHeader(emit func(Record)) {
	if d.header == nil {
		return
	}
	f := d.cur
	head := string(f.status) + " " + f.path
	if f.status == 'R' || f.status == 'C' {
		head = string(f.status) + " " + f.oldPath + " -> " + f.path
	}
	if f.binary {
		head += " (binary)"
	}
	d.header.Head = head
	d.header.ID = "file:" + f.path
	emit(*d.header)
	d.header = nil
}

func (d *diffAdapter) startHunk(n int64, text string, emit func(Record)) {
	d.emitHeader(emit)
	f := d.cur
	f.hunks++
	f.end = n
	m := hunkHeader.FindStringSubmatch(text)
	d.oldLeft, d.newLeft = 1, 1
	d.parents = 1
	if c := ccHeader.FindStringSubmatch(text); c != nil {
		// Combined: count only the result's lines.
		d.parents = len(c[1]) - 1
		d.newLeft = 1
		if c[2] != "" {
			d.newLeft, _ = strconv.ParseInt(c[2], 10, 64)
		}
		d.ccOld = d.ccOld[:0]
		for _, r := range ccRange.FindAllStringSubmatch(c[0], -1) {
			k := int64(1)
			if r[1] != "" {
				k, _ = strconv.ParseInt(r[1], 10, 64)
			}
			d.ccOld = append(d.ccOld, k)
		}
		for len(d.ccOld) < d.parents {
			d.ccOld = append(d.ccOld, 0)
		}
		d.ccOld = d.ccOld[:d.parents]
		d.sumOld()
	} else if m != nil {
		if m[2] != "" {
			d.oldLeft, _ = strconv.ParseInt(m[2], 10, 64)
		}
		if m[4] != "" {
			d.newLeft, _ = strconv.ParseInt(m[4], 10, 64)
		}
	}
	d.hunk = &Record{Kind: KindInfo, ID: "hunk:" + f.path, Head: f.path + " " + strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}}
	d.hunkKeep = d.hunkKeep[:0]
	d.added = d.added[:0]
	if d.hunkSum == nil {
		d.hunkSum = sha256.New()
	}
	d.hunkSum.Reset()
}

func (d *diffAdapter) hunkLine(n int64, text string) {
	d.hunk.addLine(n)
	d.cur.end = n
	if text == "" {
		// An empty context line some tools write without its space.
		text = " "
	}
	if d.parents > 1 {
		d.ccLine(text)
		return
	}
	switch text[0] {
	case ' ':
		d.oldLeft--
		d.newLeft--
	case '-':
		d.oldLeft--
		d.cur.dels++
		d.keepLine(text)
	case '+':
		d.newLeft--
		d.cur.adds++
		d.keepLine(text)
		if len(d.added) < 4096 {
			d.added = append(d.added, text[1:])
		}
	case '\\':
	default:
		// A line that doesn't belong in a hunk: the counts were wrong.
		d.oldLeft, d.newLeft = 0, 0
	}
}

// sumOld sets oldLeft to the lines still due from any parent.
func (d *diffAdapter) sumOld() {
	d.oldLeft = 0
	for _, k := range d.ccOld {
		if k > 0 {
			d.oldLeft += k
		}
	}
}

// ccLine counts a line of a combined hunk: one prefix column per parent.
func (d *diffAdapter) ccLine(text string) {
	if text[0] == '\\' {
		return
	}
	if len(text) < d.parents || strings.Trim(text[:d.parents], " +-") != "" {
		// Not a hunk line: the counts were wrong.
		d.newLeft, d.oldLeft = 0, 0
		return
	}
	cols := text[:d.parents]
	// A line some parent removed is in those parents only; any other line
	// is in the result, and in each parent whose column is blank.
	removed := strings.Contains(cols, "-")
	for i := 0; i < d.parents; i++ {
		if removed && cols[i] == '-' || !removed && cols[i] == ' ' {
			d.ccOld[i]--
		}
	}
	d.sumOld()
	switch {
	case removed:
		d.cur.dels++
		d.keepLine(text)
	case strings.Contains(cols, "+"):
		d.newLeft--
		d.cur.adds++
		d.keepLine(text)
		if len(d.added) < 4096 {
			d.added = append(d.added, text[d.parents:])
		}
	default:
		d.newLeft--
	}
}

func (d *diffAdapter) keepLine(text string) {
	d.hunkSum.Write([]byte(text))
	d.hunkSum.Write([]byte{0})
	if len(d.hunkKeep) < maxDetail {
		d.hunkKeep = append(d.hunkKeep, strings.TrimRight(text, "\r"))
	}
}

func (d *diffAdapter) endHunk(emit func(Record)) {
	h := d.hunk
	d.hunk = nil
	h.Detail = append([]string(nil), d.hunkKeep...)
	// Merge conflict markers added are an error; trailing whitespace added
	// a warning (what git diff --check reports).
	open, closed := false, false
	for _, a := range d.added {
		if strings.HasPrefix(a, "<<<<<<< ") {
			open = true
		}
		if strings.HasPrefix(a, ">>>>>>> ") {
			closed = true
		}
		if h.Kind == KindInfo && a != strings.TrimRight(a, " \t") {
			h.Kind = KindWarn
		}
	}
	if open && closed {
		h.Kind = KindError
	}
	// The same change in several places is one group.
	// The identity covers every changed line, not only those kept, and a
	// generated file's change never groups with another file's.
	h.GroupKey = "hunk\x00" + hex.EncodeToString(d.hunkSum.Sum(nil))
	if generatedFile(d.cur.path) || d.cur.binary {
		h.GroupKey = "gen\x00" + h.GroupKey
	}
	emit(*h)
}

func (d *diffAdapter) endFile(emit func(Record)) {
	if d.hunk != nil {
		d.endHunk(emit)
	}
	if d.cur == nil {
		return
	}
	d.emitHeader(emit)
	f := d.cur
	f.generated = generatedFile(f.path)
	d.files = append(d.files, *f)
	d.cur = nil
}

func (d *diffAdapter) endCommit(emit func(Record)) {
	if d.commit != nil {
		emit(*d.commit)
		d.commit = nil
	}
}

func (d *diffAdapter) finish(emit func(Record)) []string {
	if d.pendingP != nil {
		emit(*d.pendingP)
		d.pendingP = nil
	}
	d.endFile(emit)
	d.endCommit(emit)
	return []string{d.summary()}
}

func (d *diffAdapter) summary() string {
	var adds, dels int64
	count := map[byte]int{}
	binary, generated := 0, 0
	for _, f := range d.files {
		adds += f.adds
		dels += f.dels
		count[f.status]++
		if f.binary {
			binary++
		}
		if f.generated {
			generated++
		}
	}
	s := "diff: " + plural(int64(len(d.files)), "file", "files") + " changed, +" + group3(adds) + " -" + group3(dels)
	var parts []string
	for _, p := range []struct {
		c    byte
		word string
	}{{'A', "added"}, {'D', "deleted"}, {'R', "renamed"}, {'C', "copied"}} {
		if count[p.c] > 0 {
			parts = append(parts, group3(int64(count[p.c]))+" "+p.word)
		}
	}
	if binary > 0 {
		parts = append(parts, group3(int64(binary))+" binary")
	}
	if generated > 0 {
		parts = append(parts, group3(int64(generated))+" generated")
	}
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	if d.commits > 0 {
		s += "; " + plural(int64(d.commits), "commit", "commits")
	}
	return s
}
