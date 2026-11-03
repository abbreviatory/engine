package abbreviatory

import (
	"regexp"
	"sort"
	"strings"
)

// The JVM build tools: Gradle's console and Maven's, with javac's and
// kotlinc's diagnostics and failed tests in them.

var (
	javacDiag  = regexp.MustCompile(`^(.+\.(?:java|groovy|scala)):([0-9]+): (error|warning): (.*)$`)
	kotlinDiag = regexp.MustCompile(`^([ew]): (?:file://)?(.+\.kts?):([0-9]+):([0-9]+):? (.*)$`)
	// kotlinOld is the form before Kotlin 1.8: "e: /src/Shop.kt: (12, 5): ...".
	kotlinOld  = regexp.MustCompile(`^([ew]): (.+\.kts?): \(([0-9]+), ([0-9]+)\): (.*)$`)
	gradleTask = regexp.MustCompile(`^> Task :\S+`)
	gradleTest = regexp.MustCompile(`^(\S.*?) > (.+) (FAILED|PASSED|SKIPPED)$`)
	gradleDone = regexp.MustCompile(`^[0-9]+ tests? completed, [0-9]+ failed`)
	gradleEnd  = regexp.MustCompile(`^BUILD (FAILED|SUCCESSFUL) in `)
)

type gradle struct {
	b       block
	section string          // "", "wrong" (* What went wrong), "try" (* Try, * Get more help)
	sawPass bool            // a PASSED line: Gradle's test logging names passing tests
	task    string          // the test task running (":test", ":core:test")
	ran     map[string]bool // test tasks that ran
	summary []string
}

func newGradle() *gradle { return &gradle{} }

func (g *gradle) line(n int64, text string, emit func(Record)) {
	if g.section == "wrong" && g.b.open() && !gradleEnd.MatchString(text) && !(strings.HasPrefix(text, "* ") && strings.HasSuffix(text, ":")) {
		// Everything up to "* Try:" is the failure, the compiler's
		// messages repeated in it included.
		if g.b.pend.Head == "* What went wrong:" && strings.TrimSpace(text) != "" {
			// Show what failed, not the heading. Its identity is that line
			// alone: the compiler's messages repeated under it change
			// whenever one of them does, and are items of their own.
			g.b.pend.Head = strings.TrimSpace(text)
			g.b.pend.GroupKey = "wrong\x00" + g.b.pend.Head
			g.b.pend.addLine(n)
			return
		}
		g.b.add(n, text)
		return
	}
	if gradleTask.MatchString(text) {
		// A task starts: the "Try" advice of an earlier build is over, and
		// a test task names the tests that follow.
		g.section = ""
		task := strings.Fields(text)[2]
		if last := task[strings.LastIndexByte(task, ':')+1:]; last == "test" || strings.HasSuffix(last, "Test") {
			g.task = task
			if g.ran == nil {
				g.ran = map[string]bool{}
			}
			g.ran[task] = true
		}
	}
	switch {
	case strings.HasPrefix(text, " ") || strings.HasPrefix(text, "\t"):
		// Indented lines continue a block; they never start one.
	case javacDiag.MatchString(text):
		m := javacDiag.FindStringSubmatch(text)
		g.b.start(n, levelKind(m[3]), m[1]+":"+m[2]+" "+m[3]+": "+m[4], emit)
		g.section = ""
		return
	case kotlinDiag.MatchString(text) || kotlinOld.MatchString(text):
		m := kotlinDiag.FindStringSubmatch(text)
		if m == nil {
			m = kotlinOld.FindStringSubmatch(text)
		}
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
			g.sawPass = true
		case "SKIPPED":
			k = KindSkip
		}
		name := m[1] + " > " + m[2]
		g.b.start(n, k, name, emit)
		// Known by its task too: modules of one build often share test
		// names. The task is its "package" in run history.
		g.b.pend.ID, g.b.pend.Name = name, name
		if g.task != "" {
			g.b.pend.ID = g.task + "." + name
		}
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

// unnamedPasses: Gradle names passing tests only when its test logging is
// set to; without it, a test missing from a run passed if its test task ran.
// A run whose tests never ran (a compile error) claims nothing.
func (g *gradle) unnamedPasses() bool { return !g.sawPass && len(g.ran) > 0 }

func (g *gradle) ranPackages() []string { return sortedKeys(g.ran) }

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
	// mvnPlugin is a plugin's execution line: "[INFO] --- compiler:3.13.0:compile
	// (default-compile) @ shop ---".
	mvnPlugin   = regexp.MustCompile(`^\[INFO\] --- (\S+) \(\S+\) @ (\S+) ---$`)
	mvnTestsRun = regexp.MustCompile(`^Tests run: [0-9]+, Failures: [0-9]+, Errors: [0-9]+, Skipped: [0-9]+`)
)

// maven reads Maven's console: every line has a level in brackets, which
// gives the kind; compiler messages, failed tests with their stack traces and
// "Failed to execute goal" blocks keep their following lines together.
type maven struct {
	b       block
	cont    string // what continues the open block: "indent" or "error"
	results bool   // in Surefire's "Results:" section
	module  string // the module whose tests Surefire or Failsafe is running
	ran     map[string]bool
	summary []string
}

func newMaven() *maven { return &maven{} }

func (m *maven) line(n int64, text string, emit func(Record)) {
	lv := mvnLevel.FindStringSubmatch(text)
	level, msg := "", text
	if lv != nil {
		level, msg = lv[1], lv[2]
	}
	// "Failed to execute goal" repeats the compiler's errors under it: they
	// belong to that block, not new items. Maven's closing advice doesn't.
	if m.b.open() && m.cont == "error" && level == "ERROR" && !mvnBoiler(msg) && !strings.HasPrefix(msg, "->") &&
		!strings.HasPrefix(msg, "Failed to execute goal") {
		if strings.TrimSpace(msg) == "" {
			m.b.pend.addLine(n)
		} else {
			m.b.add(n, text)
		}
		return
	}
	if lv != nil && strings.TrimSpace(msg) == "" {
		// A level with nothing after it spaces the output out.
		m.b.flush(emit)
		infoLine(n, "", emit)
		return
	}
	// Lines that start a block.
	if d := mvnDiag.FindStringSubmatch(msg); d != nil && (level == "ERROR" || level == "WARNING" || level == "WARN") {
		word := "error"
		if level != "ERROR" {
			word = "warning"
		}
		m.b.start(n, levelKind(level), d[1]+":"+d[2]+":"+d[3]+" "+word+": "+d[4], emit)
		m.cont, m.results = "indent", false
		return
	}
	if p := mvnPlugin.FindStringSubmatch(text); p != nil {
		m.module = ""
		if strings.HasPrefix(p[1], "surefire:") || strings.HasPrefix(p[1], "failsafe:") {
			m.module = p[2]
		}
	}
	if t := mvnTestFail.FindStringSubmatch(msg); t != nil {
		m.startTest(n, t[1], emit)
		return
	}
	if t := mvnTestOld.FindStringSubmatch(msg); t != nil {
		m.startTest(n, t[2]+"."+t[1], emit)
		return
	}
	if level == "ERROR" && strings.HasPrefix(msg, "Failed to execute goal") {
		m.b.start(n, KindError, msg, emit)
		// Known by this line alone, not by the messages Maven repeats
		// under it.
		m.b.pend.GroupKey = "goal\x00" + msg
		m.cont, m.results = "error", false
		return
	}
	if m.b.open() {
		switch m.cont {
		case "indent":
			// javac's notes ("symbol:", "location:", "missing type
			// arguments ..."): indented, with or without a level.
			if strings.HasPrefix(msg, "  ") && strings.TrimSpace(msg) != "" && !mvnDiag.MatchString(strings.TrimSpace(msg)) {
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
		}
		m.b.flush(emit)
	}
	switch {
	case msg == "Results:":
		m.results = true
		infoLine(n, text, emit)
	case mvnTestsRun.MatchString(msg):
		if m.module != "" {
			if m.ran == nil {
				m.ran = map[string]bool{}
			}
			m.ran[m.module] = true
		}
		if m.results {
			m.summary = append(m.summary, msg)
		}
		infoLine(n, text, emit)
	case msg == "BUILD FAILURE" || msg == "BUILD SUCCESS":
		m.results = false
		m.summary = append(m.summary, msg)
		infoLine(n, text, emit)
	case lv != nil && (mvnBoiler(msg) || strings.TrimSpace(msg) == "" || strings.HasPrefix(msg, "-> [Help")):
		// Maven's closing advice, its headings over the compiler's messages
		// and its empty lines.
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

// mvnBoiler is a line Maven prints after every failure, or a heading over
// the compiler's messages.
func mvnBoiler(msg string) bool {
	msg = strings.TrimSpace(msg)
	for _, p := range []string{"To see the full stack trace of the errors", "Re-run Maven using the -X switch",
		"For more information about the errors and possible solutions", "[Help 1] http", "After correcting the problems, you can resume the build",
		"COMPILATION ERROR :", "COMPILATION WARNING :", "mvn <args> -rf "} {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// startTest begins a failed test, known by its module too (the "package"
// run history checks against the modules whose tests ran).
func (m *maven) startTest(n int64, name string, emit func(Record)) {
	m.b.start(n, KindFail, name, emit)
	m.b.pend.ID, m.b.pend.Name = name, name
	if m.module != "" {
		m.b.pend.ID = m.module + "." + name
	}
	m.cont = "trace"
}

// unnamedPasses: Maven names only the tests that failed, so a test missing
// from a run passed if its module's tests ran. A run whose tests never ran
// (a compile error, a skipped module) claims nothing.
func (m *maven) unnamedPasses() bool { return len(m.ran) > 0 }

func (m *maven) ranPackages() []string { return sortedKeys(m.ran) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// looksMaven reports Maven's console by lines only Maven prints; a count of
// "[INFO]" lines isn't enough, many logs have them.
func looksMaven(sample []byte) bool {
	for _, l := range strings.SplitN(string(sample), "\n", 4000) {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "[INFO] Scanning for projects...") || l == "[INFO] BUILD SUCCESS" || l == "[INFO] BUILD FAILURE" ||
			mvnPlugin.MatchString(l) || strings.HasPrefix(l, "[ERROR] Failed to execute goal ") || strings.HasPrefix(l, "[INFO] Reactor Summary") {
			return true
		}
	}
	return false
}
