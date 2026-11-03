package abbreviatory

import (
	"regexp"
	"strings"
)

// The JVM build tools: Gradle's console and Maven's, with javac's and
// kotlinc's diagnostics and failed tests in them.

var (
	javacDiag  = regexp.MustCompile(`^(.+\.(?:java|groovy|scala)):([0-9]+): (error|warning): (.*)$`)
	kotlinDiag = regexp.MustCompile(`^([ew]): (?:file://)?(.+\.kts?):([0-9]+):([0-9]+):? (.*)$`)
	gradleTask = regexp.MustCompile(`^> Task :\S+`)
	gradleTest = regexp.MustCompile(`^(\S.*?) > (.+) (FAILED|PASSED|SKIPPED)$`)
	gradleDone = regexp.MustCompile(`^[0-9]+ tests? completed, [0-9]+ failed`)
	gradleEnd  = regexp.MustCompile(`^BUILD (FAILED|SUCCESSFUL) in `)
)

type gradle struct {
	b       block
	section string // "", "wrong" (* What went wrong), "try" (* Try, * Get more help)
	summary []string
}

func newGradle() *gradle { return &gradle{} }

func (g *gradle) line(n int64, text string, emit func(Record)) {
	if g.section == "wrong" && g.b.open() && !gradleEnd.MatchString(text) && !(strings.HasPrefix(text, "* ") && strings.HasSuffix(text, ":")) {
		// Everything up to "* Try:" is the failure, the compiler's
		// messages repeated in it included.
		if g.b.pend.Head == "* What went wrong:" && strings.TrimSpace(text) != "" {
			// Show what failed, not the heading.
			g.b.pend.Head = strings.TrimSpace(text)
		}
		g.b.add(n, text)
		return
	}
	switch {
	case strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t"):
		// Indented lines continue a block; they never start one.
	case javacDiag.MatchString(text):
		m := javacDiag.FindStringSubmatch(text)
		g.b.start(n, levelKind(m[3]), m[1]+":"+m[2]+" "+m[3]+": "+m[4], emit)
		g.section = ""
		return
	case kotlinDiag.MatchString(text):
		m := kotlinDiag.FindStringSubmatch(text)
		level := "error"
		if m[1] == "w" {
			level = "warning"
		}
		g.b.start(n, levelKind(level), m[2]+":"+m[3]+":"+m[4]+" "+level+": "+m[5], emit)
		g.section = ""
		return
	case gradleTest.MatchString(text):
		m := gradleTest.FindStringSubmatch(text)
		k := KindFail
		switch m[3] {
		case "PASSED":
			k = KindPass
		case "SKIPPED":
			k = KindSkip
		}
		g.b.start(n, k, m[1]+" > "+m[2], emit)
		g.b.pend.ID = m[1] + " > " + m[2]
		g.section = ""
		return
	case text == "* What went wrong:":
		// The failure, in Gradle's words, up to "* Try:": one error.
		g.b.start(n, KindError, text, emit)
		g.section = "wrong"
		return
	case strings.HasPrefix(text, "* ") && strings.HasSuffix(text, ":") && g.section != "":
		g.b.flush(emit)
		g.section = "try"
		infoLine(n, text, emit)
		return
	}
	if g.b.open() {
		if g.section == "wrong" {
			g.b.flush(emit)
			g.section = ""
		} else if strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t") {
			// The code, the caret, "symbol:" and "location:", or a failed
			// test's exception.
			g.b.add(n, text)
			if g.b.pend.Kind == KindFail && len(g.b.pend.Detail) == 1 {
				g.b.pend.Head += " - " + strings.TrimSpace(text)
			}
			return
		} else {
			g.b.flush(emit)
		}
	}
	switch {
	case gradleDone.MatchString(text) || gradleEnd.MatchString(text):
		g.summary = append(g.summary, text)
		infoLine(n, text, emit)
	case gradleTask.MatchString(text) || g.section == "try" || isCompilerCount(text) ||
		strings.HasPrefix(text, "Deprecated Gradle features") || strings.HasPrefix(text, "You can use '--warning-mode") ||
		strings.HasPrefix(text, "For more on this, please refer to") || strings.Contains(text, "actionable task") ||
		strings.HasPrefix(text, "[Incubating] Problems report") || strings.HasPrefix(text, "Starting a Gradle Daemon") ||
		text == "FAILURE: Build failed with an exception." || strings.HasPrefix(text, "FAILURE: Build completed with"):
		infoLine(n, text, emit)
	default:
		plainLine(n, text, emit)
	}
}

func (g *gradle) finish(emit func(Record)) []string {
	g.b.flush(emit)
	return g.summary
}

// isCompilerCount is javac's "3 errors" or "4 warnings" line.
func isCompilerCount(s string) bool {
	f := strings.Fields(s)
	return len(f) == 2 && allDigits(f[0]) && (f[1] == "error" || f[1] == "errors" || f[1] == "warning" || f[1] == "warnings")
}

func levelKind(level string) Kind {
	switch strings.ToLower(level) {
	case "error":
		return KindError
	case "warning", "warn":
		return KindWarn
	}
	return KindInfo
}

func looksGradle(sample []byte) bool {
	for _, l := range strings.SplitN(string(sample), "\n", 1000) {
		l = strings.TrimRight(l, "\r")
		if gradleTask.MatchString(l) || gradleEnd.MatchString(l) || l == "FAILURE: Build failed with an exception." {
			return true
		}
	}
	return false
}

var (
	mvnLevel    = regexp.MustCompile(`^\[(INFO|WARNING|WARN|ERROR|DEBUG)\] ?(.*)$`)
	mvnDiag     = regexp.MustCompile(`^(.+\.(?:java|kt|groovy|scala)):\[([0-9]+),([0-9]+)\] (.*)$`)
	mvnTestFail = regexp.MustCompile(`^(\S+) +-- Time elapsed: .*<<< (FAILURE|ERROR)!$`)
	mvnTestOld  = regexp.MustCompile(`^(\S+)\((\S+)\) +Time elapsed: .*<<< (FAILURE|ERROR)!$`)
	mvnTestsRun = regexp.MustCompile(`^Tests run: [0-9]+, Failures: [0-9]+, Errors: [0-9]+, Skipped: [0-9]+`)
)

// maven reads Maven's console: every line has a level in brackets, which
// gives the kind; compiler messages, failed tests with their stack traces and
// "Failed to execute goal" blocks keep their following lines together.
type maven struct {
	b       block
	cont    string // what continues the open block: "indent" or "error"
	results bool   // in Surefire's "Results:" section
	summary []string
}

func newMaven() *maven { return &maven{} }

func (m *maven) line(n int64, text string, emit func(Record)) {
	lv := mvnLevel.FindStringSubmatch(text)
	level, msg := "", text
	if lv != nil {
		level, msg = lv[1], lv[2]
	}
	// Lines that start a block.
	if d := mvnDiag.FindStringSubmatch(msg); d != nil && (level == "ERROR" || level == "WARNING" || level == "WARN") {
		m.b.start(n, levelKind(level), d[1]+":"+d[2]+":"+d[3]+" "+strings.ToLower(strings.TrimSuffix(level, "ING"))+": "+d[4], emit)
		m.cont, m.results = "indent", false
		return
	}
	if t := mvnTestFail.FindStringSubmatch(msg); t != nil {
		m.b.start(n, KindFail, t[1], emit)
		m.b.pend.ID = t[1]
		m.cont = "trace"
		return
	}
	if t := mvnTestOld.FindStringSubmatch(msg); t != nil {
		m.b.start(n, KindFail, t[2]+"."+t[1], emit)
		m.b.pend.ID = t[2] + "." + t[1]
		m.cont = "trace"
		return
	}
	if level == "ERROR" && strings.HasPrefix(msg, "Failed to execute goal") {
		m.b.start(n, KindError, msg, emit)
		m.cont, m.results = "error", false
		return
	}
	if m.b.open() {
		switch m.cont {
		case "indent":
			if lv != nil && strings.HasPrefix(msg, "  ") && !mvnDiag.MatchString(strings.TrimSpace(msg)) {
				m.b.add(n, text)
				return
			}
		case "trace":
			// The exception and its frames have no level.
			if lv == nil && strings.TrimSpace(text) != "" {
				m.b.add(n, text)
				if len(m.b.pend.Detail) == 1 {
					m.b.pend.Head += " - " + strings.TrimSpace(text)
				}
				return
			}
		case "error":
			if level == "ERROR" && !strings.HasPrefix(msg, "->") {
				m.b.add(n, text)
				return
			}
		}
		m.b.flush(emit)
	}
	switch {
	case msg == "Results:":
		m.results = true
		infoLine(n, text, emit)
	case mvnTestsRun.MatchString(msg):
		if m.results {
			m.summary = append(m.summary, msg)
		}
		infoLine(n, text, emit)
	case msg == "BUILD FAILURE" || msg == "BUILD SUCCESS":
		m.results = false
		m.summary = append(m.summary, msg)
		infoLine(n, text, emit)
	case m.results && (level == "ERROR" || level == "WARNING"):
		// The list of failed tests repeats the ones already read.
		infoLine(n, text, emit)
	case level == "ERROR":
		emit(Record{Kind: KindError, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
	case level == "WARNING" || level == "WARN":
		emit(Record{Kind: KindWarn, Head: strings.TrimRight(text, " \t"), Lines: []Range{{n, n}}})
	case level != "":
		infoLine(n, text, emit)
	default:
		plainLine(n, text, emit)
	}
}

func (m *maven) finish(emit func(Record)) []string {
	m.b.flush(emit)
	return m.summary
}

func looksMaven(sample []byte) bool {
	levels := 0
	for _, l := range strings.SplitN(string(sample), "\n", 400) {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "[INFO] Scanning for projects") || strings.HasPrefix(l, "[INFO] BUILD ") ||
			strings.HasPrefix(l, "[INFO] --- ") || strings.HasPrefix(l, "[ERROR] Failed to execute goal") {
			return true
		}
		if mvnLevel.MatchString(l) {
			levels++
		}
	}
	return levels >= 20
}
