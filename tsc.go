package abbreviatory

import (
	"regexp"
	"strings"
)

// TypeScript's compiler, tsc, in both of its styles: plain, as in CI
// ("src/cart.ts(9,7): error TS2322: ..."), and pretty, as at a terminal
// ("src/cart.ts:9:7 - error TS2322: ..." with the code under it).

var (
	tscPlain  = regexp.MustCompile(`^(.+?)\(([0-9]+),([0-9]+)\): (error|warning|message) (TS[0-9]+): (.*)$`)
	tscPretty = regexp.MustCompile(`^(.+?):([0-9]+):([0-9]+) - (error|warning|message) (TS[0-9]+): (.*)$`)
	tscFound  = regexp.MustCompile(`^(?:\[[^\]]{1,40}\] )?Found [0-9]+ errors?\b`)
	// tscStamp starts a line of tsc --watch or -b --verbose: "[10:00:01 AM] ".
	tscStamp = regexp.MustCompile(`^\[[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?: ?[AaPp][Mm])?\] `)
	// A diagnostic without a file, such as a bad tsconfig.json.
	tscBare = regexp.MustCompile(`^(error|warning) (TS[0-9]+): (.*)$`)
)

type tsc struct {
	b       block
	pretty  bool // the open block is a pretty one, which runs over blank lines
	table   bool // in the "Errors  Files" table after the count
	summary []string
}

func newTsc() *tsc { return &tsc{} }

func tscKind(level string) Kind {
	switch level {
	case "error":
		return KindError
	case "warning":
		return KindWarn
	}
	return KindInfo
}

func (t *tsc) line(n int64, text string, emit func(Record)) {
	if m := tscPlain.FindStringSubmatch(text); m != nil {
		t.b.start(n, tscKind(m[4]), m[1]+":"+m[2]+":"+m[3]+" "+m[4]+" "+m[5]+": "+m[6], emit)
		t.pretty, t.table = false, false
		return
	}
	if m := tscPretty.FindStringSubmatch(text); m != nil && !strings.HasPrefix(text, " ") {
		t.b.start(n, tscKind(m[4]), m[1]+":"+m[2]+":"+m[3]+" "+m[4]+" "+m[5]+": "+m[6], emit)
		t.pretty, t.table = true, false
		return
	}
	if m := tscBare.FindStringSubmatch(text); m != nil {
		t.b.start(n, tscKind(m[1]), text, emit)
		t.pretty, t.table = false, false
		return
	}
	if tscFound.MatchString(text) {
		t.b.flush(emit)
		// In watch mode every compilation ends with a count; the last one
		// is the state the output ends in.
		t.summary = []string{text}
		infoLine(n, text, emit)
		return
	}
	if text == "Errors  Files" {
		t.b.flush(emit)
		t.table = true
		infoLine(n, text, emit)
		return
	}
	if tscStamp.MatchString(text) {
		// A new compilation in watch mode, or a build step: information,
		// and the end of the diagnostic before it.
		t.b.flush(emit)
		t.table = false
		infoLine(n, text, emit)
		return
	}
	if t.b.open() {
		// A plain diagnostic continues on indented lines (the chain of
		// reasons); a pretty one on everything up to the next diagnostic.
		if t.pretty || strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") {
			t.b.add(n, text)
			return
		}
		t.b.flush(emit)
	}
	if t.table || strings.HasPrefix(text, "[") && strings.Contains(text, "] ") {
		// The table of counts per file, and the timestamps of --watch.
		infoLine(n, text, emit)
		return
	}
	plainLine(n, text, emit)
}

func (t *tsc) finish(emit func(Record)) []string {
	t.b.flush(emit)
	return t.summary
}

func looksTsc(sample []byte) bool {
	for _, l := range strings.SplitN(string(sample), "\n", 400) {
		l = clean([]byte(l))
		if tscPlain.MatchString(l) || tscPretty.MatchString(l) {
			return true
		}
	}
	return false
}

// ESLint's default "stylish" output: a file's path, then one indented line
// per problem ("  12:5  error  'x' is assigned a value but never used
// no-unused-vars"), then a count. The compact and unix formats put the path
// on each line ("src/a.js:12:5: 'x' is assigned ... [Error/no-unused-vars]").

var (
	eslintProblem = regexp.MustCompile(`^\s+([0-9]+):([0-9]+)\s+(error|warning)\s+(.*?)(?:\s{2,}(\S+))?\s*$`)
	eslintFatal   = regexp.MustCompile(`^\s+([0-9]+):([0-9]+)\s+error\s+Parsing error: (.*)$`)
	eslintCount   = regexp.MustCompile(`^(✖|\?) [0-9]+ problems? \(`)
	eslintCompact = regexp.MustCompile(`^(.+?): line ([0-9]+), col ([0-9]+), (Error|Warning) - (.*?)(?: \(([^)]+)\))?$`)
	eslintUnix    = regexp.MustCompile(`^(.+?):([0-9]+):([0-9]+): (.*) \[(Error|Warning)(?:/([^\]]+))?\]$`)
)

type eslint struct {
	file    string
	summary []string
}

func newEslint() *eslint { return &eslint{} }

func (e *eslint) line(n int64, text string, emit func(Record)) {
	problem := func(file, line, col, level, msg, rule string) {
		k := KindError
		if strings.EqualFold(level, "warning") {
			k = KindWarn
		}
		head := file + ":" + line + ":" + col + " " + strings.ToLower(level) + " " + strings.TrimSpace(msg)
		if rule != "" {
			head += " (" + rule + ")"
		}
		emit(Record{Kind: k, Head: head, Lines: []Range{{n, n}}})
	}
	if m := eslintFatal.FindStringSubmatch(text); m != nil && e.file != "" {
		problem(e.file, m[1], m[2], "error", "Parsing error: "+m[3], "")
		return
	}
	if m := eslintProblem.FindStringSubmatch(text); m != nil && e.file != "" {
		problem(e.file, m[1], m[2], m[3], m[4], m[5])
		return
	}
	if m := eslintCompact.FindStringSubmatch(text); m != nil {
		problem(m[1], m[2], m[3], m[4], m[5], m[6])
		return
	}
	if m := eslintUnix.FindStringSubmatch(text); m != nil {
		problem(m[1], m[2], m[3], m[5], m[4], m[6])
		return
	}
	if eslintCount.MatchString(text) {
		e.summary = append(e.summary, text)
		infoLine(n, text, emit)
		return
	}
	t := strings.TrimSpace(text)
	if t != "" && !strings.HasPrefix(text, " ") && !strings.HasPrefix(text, "\t") && looksPath(t) {
		// The file the problems below belong to.
		e.file = t
		infoLine(n, text, emit)
		return
	}
	if strings.Contains(text, "potentially fixable with the `--fix` option") {
		infoLine(n, text, emit)
		return
	}
	plainLine(n, text, emit)
}

func (e *eslint) finish(emit func(Record)) []string { return e.summary }

// looksPath reports a line that is a file path on its own: an absolute path
// (ESLint's stylish output prints those, extension or not), or a relative one
// whose file name has an extension.
func looksPath(s string) bool {
	if strings.ContainsAny(s, " \t") && !strings.Contains(s, "/") && !strings.Contains(s, "\\") {
		return false
	}
	abs := strings.HasPrefix(s, "/") || len(s) > 2 && s[1] == ':' && (s[2] == '\\' || s[2] == '/')
	if abs && !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ":") {
		return true
	}
	return (strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") || strings.Contains(s, "/")) &&
		strings.LastIndex(s, ".") > strings.LastIndexAny(s, "/\\")
}

func looksEslint(sample []byte) bool {
	s := string(sample)
	if eslintCount.MatchString(s) || strings.Contains(s, "\n✖ ") && strings.Contains(s, " problem") {
		return true
	}
	lines := strings.SplitN(s, "\n", 400)
	for i, l := range lines {
		l = strings.TrimRight(l, "\r")
		if eslintCompact.MatchString(l) || eslintUnix.MatchString(l) {
			return true
		}
		if looksPath(strings.TrimSpace(l)) && !strings.HasPrefix(l, " ") && i+1 < len(lines) && eslintProblem.MatchString(strings.TrimRight(lines[i+1], "\r")) {
			return true
		}
	}
	return false
}
