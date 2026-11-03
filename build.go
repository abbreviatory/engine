package abbreviatory

import (
	"bytes"
	"strings"
)

// FamilyBuild holds the build tools' output: compiler and linter
// diagnostics, build tool consoles and package manager errors.
const FamilyBuild = "build"

// block collects the lines of one multi-line record (a compiler diagnostic
// with its code snippet, a package manager's error block).
type block struct {
	pend *Record
	more int64 // lines past maxDetail
}

func (b *block) open() bool { return b.pend != nil }

// start begins a record with the given kind and head on line n, emitting
// the one before.
func (b *block) start(n int64, k Kind, head string, emit func(Record)) {
	b.flush(emit)
	b.pend = &Record{Kind: k, Head: strings.TrimRight(head, " \t"), Lines: []Range{{n, n}}}
}

// add puts line n into the open record's detail. Blank lines belong to the
// record but aren't worth showing.
func (b *block) add(n int64, text string) {
	if strings.TrimSpace(text) == "" {
		b.pend.addLine(n)
		return
	}
	if len(b.pend.Detail) < maxDetail {
		b.pend.Detail = append(b.pend.Detail, strings.TrimRight(text, " \t"))
	} else {
		b.more++
	}
	b.pend.addLine(n)
}

func (b *block) flush(emit func(Record)) {
	if b.pend == nil {
		return
	}
	r := *b.pend
	if b.more > 0 {
		r.Detail = append(r.Detail, "... ("+plural(b.more, "more line", "more lines")+")")
	}
	emit(r)
	b.pend, b.more = nil, 0
}

// plainLine is a line no block claims: a record of its own, at the level its
// words give it, as in a log.
func plainLine(n int64, text string, emit func(Record)) {
	if strings.TrimSpace(text) == "" {
		emit(Record{Kind: KindInfo, Head: "(blank line)", Lines: []Range{{n, n}}})
		return
	}
	emit(Record{Kind: levelOf(text), Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
}

// infoLine is a line kept as information whatever its words say (a build
// tool's status line, a count it printed).
func infoLine(n int64, text string, emit func(Record)) {
	if strings.TrimSpace(text) == "" {
		emit(Record{Kind: KindInfo, Head: "(blank line)", Lines: []Range{{n, n}}})
		return
	}
	emit(Record{Kind: KindInfo, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
}

// detectBuild names the build tool whose output the sample is, or "".
func detectBuild(sample []byte) string {
	sample = cleanLines(sample)
	switch {
	case looksCargo(sample):
		return "cargo"
	case looksTsc(sample):
		return "tsc"
	case looksEslint(sample):
		return "eslint"
	case looksGradle(sample):
		return "gradle"
	case looksMaven(sample):
		return "maven"
	case looksNpm(sample):
		return "npm"
	}
	return ""
}

// cleanLines is the sample with colour codes and other control characters
// taken out of each line, as the adapters see it: cargo, ESLint and others
// colour their output when told to (CARGO_TERM_COLOR=always in CI).
func cleanLines(sample []byte) []byte {
	if !bytes.ContainsRune(sample, 0x1b) {
		return sample
	}
	lines := bytes.Split(sample, []byte("\n"))
	out := make([]byte, 0, len(sample))
	for i, l := range lines {
		if i > 0 {
			out = append(out, '\n')
		}
		out = append(out, clean(l)...)
	}
	return out
}

// npm and pnpm: install and run output. An npm error is a run of "npm
// error" lines (one record, headed by its code and first message); a pnpm
// error is an ERR_PNPM_ line with the explanation under it. Warnings are a
// record each; progress and the update box are information.

type npm struct {
	b        block
	kind     string // what the open block is: "npm" or "pnpm"
	headDone bool   // the open npm block's head has its message
	summary  []string
}

func newNpm() *npm { return &npm{} }

func npmErrorLine(s string) (string, bool) {
	for _, p := range []string{"npm error ", "npm ERR! "} {
		if strings.HasPrefix(s, p) {
			return s[len(p):], true
		}
	}
	if s == "npm error" || s == "npm ERR!" {
		return "", true
	}
	return "", false
}

func isNpmWarn(s string) bool {
	return strings.HasPrefix(s, "npm warn ") || strings.HasPrefix(s, "npm WARN ") ||
		strings.HasPrefix(s, " WARN  ") || strings.HasPrefix(s, "WARN  ")
}

func isPnpmError(s string) bool {
	t := strings.TrimLeft(s, " ")
	return strings.HasPrefix(t, "ERR_PNPM_") || strings.HasPrefix(t, "ELIFECYCLE ")
}

func (p *npm) line(n int64, text string, emit func(Record)) {
	if msg, ok := npmErrorLine(text); ok {
		// A code line starts the next error, even right after another one.
		if !p.b.open() || p.kind != "npm" || strings.HasPrefix(msg, "code ") {
			p.b.start(n, KindError, text, emit)
			p.kind, p.headDone = "npm", false
			return
		}
		pend := p.b.pend
		// The head: the code with the first message after it.
		if t := strings.TrimSpace(msg); t != "" && !p.headDone && (strings.HasPrefix(pend.Head, "npm error code ") || strings.HasPrefix(pend.Head, "npm ERR! code ")) {
			code := strings.TrimSpace(pend.Head[strings.Index(pend.Head, "code ")+5:])
			if t != code && !allDigits(t) {
				pend.Head += ": " + strings.TrimSpace(strings.TrimPrefix(t, code))
				p.headDone = true
			}
		}
		if strings.TrimSpace(msg) == "" {
			// A bare "npm error" spaces the block out; nothing to show.
			pend.addLine(n)
			return
		}
		p.b.add(n, text)
		return
	}
	if isPnpmError(text) {
		p.b.start(n, KindError, strings.TrimSpace(text), emit)
		p.kind = "pnpm"
		return
	}
	if p.b.open() {
		if p.kind == "pnpm" && !isNpmWarn(text) && !strings.HasPrefix(text, "Progress: ") && !strings.HasPrefix(text, "Done in ") {
			// pnpm explains an error in plain paragraphs under it.
			p.b.add(n, text)
			return
		}
		p.b.flush(emit)
	}
	switch {
	case isNpmWarn(text):
		emit(Record{Kind: KindWarn, Head: strings.TrimSpace(text), Lines: []Range{{n, n}}})
	case strings.Contains(text, " vulnerabilit") && (strings.Contains(text, "critical") || strings.Contains(text, "high") || strings.Contains(text, "moderate") || strings.Contains(text, "low")):
		p.summary = append(p.summary, strings.TrimSpace(text))
		emit(Record{Kind: KindWarn, Head: strings.TrimSpace(text), Lines: []Range{{n, n}}})
	case strings.HasPrefix(text, "added ") || strings.HasPrefix(text, "removed ") || strings.HasPrefix(text, "changed ") || strings.HasPrefix(text, "up to date") ||
		strings.HasPrefix(text, "Done in ") || strings.HasPrefix(text, "Packages: "):
		p.summary = append(p.summary, strings.TrimSpace(text))
		infoLine(n, text, emit)
	case strings.HasPrefix(text, "Progress: ") || strings.HasPrefix(text, "> ") || strings.ContainsAny(text, "╭╮╰╯│") ||
		strings.HasPrefix(text, "npm notice") || strings.HasPrefix(text, "+++") || strings.HasPrefix(text, "+ ") || strings.HasPrefix(text, "- "):
		infoLine(n, text, emit)
	default:
		plainLine(n, text, emit)
	}
}

func (p *npm) finish(emit func(Record)) []string {
	p.b.flush(emit)
	return p.summary
}

func looksNpm(sample []byte) bool {
	for _, l := range strings.SplitN(string(sample), "\n", 400) {
		l = strings.TrimRight(l, "\r")
		if _, ok := npmErrorLine(l); ok || isNpmWarn(l) && strings.HasPrefix(l, "npm ") || isPnpmError(l) && strings.HasPrefix(strings.TrimSpace(l), "ERR_PNPM_") ||
			strings.HasPrefix(l, "Progress: resolved ") {
			return true
		}
	}
	return false
}
