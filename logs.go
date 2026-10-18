package abbreviatory

import (
	"strings"
)

// levelScan is how far into a line the level is looked for. Levels sit near
// the start of a log line; looking further finds words inside messages and
// payloads.
const levelScan = 160

// logs reads application and terminal logs: one record per line, with
// continuation lines (stack traces, Python tracebacks, Go panics, wrapped
// messages) attached to the line they belong to.
type logs struct {
	pend  *Record
	mode  int // what kind of block pend is
	emitF func(Record)
}

const (
	blockNone = iota
	blockTraceback
	blockPanic
)

func newLogs() *logs { return &logs{} }

func (l *logs) flush(emit func(Record)) {
	if l.pend == nil {
		return
	}
	r := *l.pend
	if l.mode == blockTraceback && len(r.Detail) > 0 && strings.HasPrefix(r.Head, "Traceback (most recent call last)") {
		// Show the exception line, not "Traceback (most recent call
		// last):", and keep the whole trace as detail.
		last := strings.TrimSpace(r.Detail[len(r.Detail)-1])
		if isExceptionLine(last) {
			r.Detail = append([]string{r.Head}, r.Detail[:len(r.Detail)-1]...)
			r.Head = last
		}
	}
	emit(r)
	l.pend = nil
	l.mode = blockNone
}

func (l *logs) line(n int64, text string, emit func(Record)) {
	// Python's logging.exception and similar print the message, then the
	// traceback: keep them together.
	if l.pend != nil && l.mode == blockNone && l.pend.Kind >= KindWarn && len(l.pend.Detail) == 0 &&
		strings.HasPrefix(text, "Traceback (most recent call last)") {
		l.mode = blockTraceback
		l.pend.Kind = KindError
		l.pend.Detail = append(l.pend.Detail, text)
		l.pend.addLine(n)
		return
	}
	if l.pend != nil && l.continues(text) {
		l.pend.Detail = append(l.pend.Detail, strings.TrimRight(text, " \t"))
		l.pend.addLine(n)
		return
	}
	l.flush(emit)
	if strings.TrimSpace(text) == "" {
		emit(Record{Kind: KindInfo, Head: "(blank line)", Lines: []Range{{n, n}}})
		return
	}
	k := levelOf(text)
	if k == KindInfo && isExceptionLine(text) {
		k = KindError
	}
	l.pend = &Record{Kind: k, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}}
	switch {
	case strings.HasPrefix(text, "Traceback (most recent call last)"):
		l.mode = blockTraceback
	case strings.HasPrefix(text, "panic: ") || strings.HasPrefix(text, "fatal error: "):
		l.mode = blockPanic
	}
}

// continues reports whether text belongs to the pending record.
func (l *logs) continues(text string) bool {
	if text == "" || strings.TrimSpace(text) == "" {
		// Blank lines sit inside Go panics between goroutines.
		return l.mode == blockPanic
	}
	c := text[0]
	if c == ' ' || c == '\t' {
		return true
	}
	if strings.HasPrefix(text, "Caused by:") || strings.HasPrefix(text, "Suppressed:") {
		return true
	}
	// An exception printed right after an error or warning log line, as
	// Java and many other loggers do: the line names the exception, the
	// indented frames follow.
	if l.pend.Kind >= KindWarn && l.mode == blockNone && isExceptionLine(text) && len(l.pend.Detail) == 0 {
		return true
	}
	switch l.mode {
	case blockTraceback:
		// The traceback ends with the exception line, which is not
		// indented. Chained tracebacks continue with "During handling..."
		// and "The above exception...".
		if strings.HasPrefix(text, "During handling of the above exception") ||
			strings.HasPrefix(text, "The above exception was the direct cause") ||
			strings.HasPrefix(text, "Traceback (most recent call last)") {
			return true
		}
		if isExceptionLine(text) {
			last := ""
			if d := l.pend.Detail; len(d) > 0 {
				last = d[len(d)-1]
			}
			// Only the first exception line after the frames.
			return strings.HasPrefix(last, " ") || strings.HasPrefix(last, "\t")
		}
	case blockPanic:
		if strings.HasPrefix(text, "goroutine ") || strings.HasPrefix(text, "created by ") ||
			strings.HasPrefix(text, "exit status ") || strings.HasPrefix(text, "[signal ") ||
			looksLikeGoFrame(text) {
			return true
		}
	}
	return false
}

// isExceptionLine matches "ValueError: bad value", "KeyError: 'x'",
// "requests.exceptions.HTTPError: 500" and the like.
func isExceptionLine(s string) bool {
	i := strings.IndexByte(s, ':')
	name := s
	if i >= 0 {
		name = s[:i]
	}
	if name == "" || strings.ContainsAny(name, " \t") {
		return false
	}
	for _, suf := range []string{"Error", "Exception", "Exit", "Interrupt", "Warning", "Failure"} {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// looksLikeGoFrame matches a function line in a Go stack trace, such as
// "main.main()" or "net/http.(*conn).serve(0xc000123456, {0x7ff, 0xc0})".
func looksLikeGoFrame(s string) bool {
	i := strings.IndexByte(s, '(')
	if i <= 0 || !strings.HasSuffix(s, ")") {
		return false
	}
	return !strings.ContainsAny(s[:i], " \t") && strings.Contains(s[:i], ".")
}

func (l *logs) finish(emit func(Record)) []string {
	l.flush(emit)
	return nil
}

// levelOf finds the level of a log line by the usual conventions: an
// upper-case level word near the start (ERROR, WARN, FATAL...), key=value or
// JSON level fields, bracketed levels, compiler-style "error:" and
// "warning:", glog prefixes, and the first lines of panics and tracebacks.
func levelOf(s string) Kind {
	if strings.HasPrefix(s, "panic: ") || strings.HasPrefix(s, "fatal error: ") ||
		strings.HasPrefix(s, "Traceback (most recent call last)") ||
		strings.HasPrefix(s, "Exception in thread ") || strings.HasPrefix(s, "npm ERR!") {
		return KindError
	}
	p := s
	if len(p) > levelScan {
		p = p[:levelScan]
	}
	// glog / klog: E1018 12:34:56.789012 ...
	if len(p) > 6 && (p[0] == 'E' || p[0] == 'F' || p[0] == 'W') && allDigits(p[1:5]) && p[5] == ' ' {
		if p[0] == 'W' {
			return KindWarn
		}
		return KindError
	}
	best := KindInfo
	// Upper-case words.
	for i := 0; i < len(p); {
		if p[i] < 'A' || p[i] > 'Z' || (i > 0 && isLetter(p[i-1])) {
			i++
			continue
		}
		j := i
		for j < len(p) && p[j] >= 'A' && p[j] <= 'Z' {
			j++
		}
		if j == len(p) || !isLetter(p[j]) {
			if k, ok := levelWords[p[i:j]]; ok && k > best {
				best = k
			}
		}
		i = j
	}
	if best == KindError {
		return best
	}
	low := strings.ToLower(p)
	// Compiler and tool style: "error: ...", "src/x.c:3:1: error: ...",
	// "error[E0425]: ...", and the same for warnings.
	if strings.HasPrefix(low, "error:") || strings.HasPrefix(low, "error[") ||
		strings.Contains(low, ": error:") || strings.Contains(low, ": error[") ||
		strings.Contains(low, ": fatal error:") {
		return KindError
	}
	if strings.HasPrefix(low, "warning:") || strings.HasPrefix(low, "warning[") || strings.Contains(low, ": warning:") {
		best = KindWarn
	}
	if !strings.Contains(low, "lev") && !strings.Contains(low, "lvl") && !strings.Contains(low, "sever") &&
		!strings.ContainsAny(low, "[<") {
		return best
	}
	for _, f := range levelForms {
		if strings.Contains(low, f.text) && f.kind > best {
			best = f.kind
		}
	}
	return best
}

var levelWords = map[string]Kind{
	"FATAL": KindError, "PANIC": KindError, "CRITICAL": KindError, "CRIT": KindError,
	"EMERG": KindError, "EMERGENCY": KindError, "ALERT": KindError, "ERROR": KindError,
	"ERR": KindError, "SEVERE": KindError, "FAILURE": KindError,
	"WARN": KindWarn, "WARNING": KindWarn,
}

var levelForms = []struct {
	text string
	kind Kind
}{
	{"level=error", KindError}, {"level=fatal", KindError}, {"level=panic", KindError}, {"level=crit", KindError},
	{"lvl=eror", KindError}, {"lvl=crit", KindError},
	{`"level":"error"`, KindError}, {`"level":"fatal"`, KindError}, {`"level":"panic"`, KindError},
	{`"level": "error"`, KindError}, {`"level": "fatal"`, KindError},
	{`"severity":"error"`, KindError}, {`"severity": "error"`, KindError},
	{"[error]", KindError}, {"[fatal]", KindError}, {"[crit]", KindError}, {"<error>", KindError},
	{"level=warn", KindWarn}, {"lvl=warn", KindWarn},
	{`"level":"warn`, KindWarn}, {`"level": "warn`, KindWarn},
	{`"severity":"warn`, KindWarn}, {`"severity": "warn`, KindWarn},
	{"[warn]", KindWarn}, {"[warning]", KindWarn},
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func allDigits(s string) bool {
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
