package abbreviatory

import (
	"bytes"
	"sort"
	"strings"
)

// Families group formats by what they hold.
const (
	FamilyTests = "test-output"
	FamilyLogs  = "logs"
	FamilyJSON  = "json"
	// FamilyDiff, FamilyHTTP and FamilyListing hold one format each.
	FamilyDiff    = "diff"
	FamilyHTTP    = "http"
	FamilyListing = "listing"
)

// lineAdapter reads a format line by line. It turns lines into records and
// calls emit for each finished one. Every line it is given should end up in
// some record, so the reduced output can account for all of the input.
type lineAdapter interface {
	// line is called for each input line, numbered from 1. text is the
	// cleaned line (see clean).
	line(n int64, text string, emit func(Record))
	// finish is called at the end of input. It returns summary lines the
	// tool itself printed (such as pytest's final count), if any.
	finish(emit func(Record)) []string
}

type formatInfo struct {
	name    string // used with --format and in the index
	family  string
	display string // shown in the header
	// whole formats are read entirely and parsed at once; others line by line.
	whole bool
	new   func() lineAdapter
}

var formats = []formatInfo{
	{name: "gotest-json", family: FamilyTests, display: "go test -json output", new: func() lineAdapter { return newGoTestJSON() }},
	{name: "gotest", family: FamilyTests, display: "go test output", new: func() lineAdapter { return newGoTestText() }},
	{name: "pytest", family: FamilyTests, display: "pytest output", new: func() lineAdapter { return newPytest() }},
	{name: "junit", family: FamilyTests, display: "JUnit XML report", whole: true},
	{name: "diff", family: FamilyDiff, display: "diff", new: func() lineAdapter { return newDiff() }},
	{name: "http", family: FamilyHTTP, display: "HTTP response", new: func() lineAdapter { return newHTTP() }},
	{name: "listing", family: FamilyListing, display: "directory listing", new: func() lineAdapter { return newListing() }},
	{name: "logs", family: FamilyLogs, display: "log", new: func() lineAdapter { return newLogs() }},
	{name: "json", family: FamilyJSON, display: "JSON document", whole: true},
}

func lookupFormat(name string) (formatInfo, bool) {
	for _, f := range formats {
		if f.name == name {
			return f, true
		}
	}
	return formatInfo{}, false
}

// FormatNames lists the formats Reduce accepts, apart from "auto" and the
// family names.
func FormatNames() []string {
	var out []string
	for _, f := range formats {
		out = append(out, f.name)
	}
	sort.Strings(out)
	return out
}

// detect picks a format from the start of the input. want is what the caller
// asked for: "", "auto", a family name or a format name.
func detect(sample []byte, want string) (formatInfo, error) {
	switch want {
	case "", "auto":
	case FamilyTests, "test", "tests":
		if f := detectTests(sample); f != "" {
			fi, _ := lookupFormat(f)
			return fi, nil
		}
		// Not a test runner we know: treat it as a log, which still finds
		// errors, warnings and repeats.
		fi, _ := lookupFormat("logs")
		return fi, nil
	case FamilyLogs, "log", "text":
		fi, _ := lookupFormat("logs")
		return fi, nil
	default:
		fi, ok := lookupFormat(want)
		if !ok {
			return formatInfo{}, &FormatError{Name: want}
		}
		return fi, nil
	}
	if looksHTTP(sample) {
		fi, _ := lookupFormat("http")
		return fi, nil
	}
	if f := detectTests(sample); f != "" {
		fi, _ := lookupFormat(f)
		return fi, nil
	}
	if looksJSONDocument(sample) {
		fi, _ := lookupFormat("json")
		return fi, nil
	}
	if looksDiff(sample) {
		fi, _ := lookupFormat("diff")
		return fi, nil
	}
	if looksListing(sample) {
		fi, _ := lookupFormat("listing")
		return fi, nil
	}
	fi, _ := lookupFormat("logs")
	return fi, nil
}

// looksDiff reports a unified diff: a "diff --git" line, or "---" and
// "+++" lines followed by a hunk header, near the start (after a commit's
// header and message, for git show).
func looksDiff(sample []byte) bool {
	lines := bytes.SplitN(sample, []byte("\n"), 400)
	for i, l := range lines {
		l = bytes.TrimRight(l, "\r")
		if bytes.HasPrefix(l, []byte("diff --git ")) {
			return true
		}
		if bytes.HasPrefix(l, []byte("--- ")) && i+2 < len(lines) && bytes.HasPrefix(lines[i+1], []byte("+++ ")) && bytes.HasPrefix(lines[i+2], []byte("@@ -")) {
			return true
		}
	}
	return false
}

// FormatError reports an unknown --format value.
type FormatError struct{ Name string }

func (e *FormatError) Error() string {
	return "unknown format " + quote(e.Name) + "; known: auto, test-output, logs, json, " + strings.Join(FormatNames(), ", ")
}

func quote(s string) string { return "\"" + s + "\"" }

func detectTests(sample []byte) string {
	trim := bytes.TrimLeft(sample, " \t\r\n\xef\xbb\xbf")
	if bytes.HasPrefix(trim, []byte("<?xml")) || bytes.HasPrefix(trim, []byte("<testsuite")) {
		if bytes.Contains(sample, []byte("<testsuite")) || bytes.Contains(sample, []byte("<testcase")) {
			return "junit"
		}
	}
	// go test -json, even with a few plain lines first ("go: downloading
	// ...", a wrapper script): most of its lines are test events.
	events, plain := 0, 0
	for i, l := range bytes.Split(sample, []byte("\n")) {
		if i > 2000 {
			break
		}
		l = bytes.TrimSpace(l)
		if len(l) == 0 {
			continue
		}
		if (bytes.HasPrefix(l, []byte(`{"Time":`)) || bytes.HasPrefix(l, []byte(`{"ImportPath":`)) || bytes.HasPrefix(l, []byte(`{"Action":`))) &&
			bytes.Contains(l, []byte(`"Action":`)) {
			events++
		} else {
			plain++
		}
	}
	if events > 0 && events >= plain {
		return "gotest-json"
	}
	if bytes.Contains(sample, []byte("test session starts")) || bytes.Contains(sample, []byte("\n=========================== short test summary info")) {
		return "pytest"
	}
	for _, l := range bytes.Split(sample, []byte("\n")) {
		if bytes.HasPrefix(l, []byte("=== RUN ")) || bytes.HasPrefix(l, []byte("--- FAIL: ")) || bytes.HasPrefix(l, []byte("--- PASS: ")) {
			return "gotest"
		}
	}
	return ""
}

// looksJSONDocument reports whether the sample starts a single JSON object
// or array rather than JSON lines.
func looksJSONDocument(sample []byte) bool {
	trim := bytes.TrimLeft(sample, " \t\r\n\xef\xbb\xbf")
	if len(trim) == 0 || (trim[0] != '{' && trim[0] != '[') {
		return false
	}
	// JSON lines: several lines that each start with '{' and the first one
	// closes on its own line. A pretty-printed document has a lone '{' or a
	// first line that doesn't end the value.
	lines := bytes.Split(trim, []byte("\n"))
	if len(lines) >= 2 {
		first := bytes.TrimSpace(lines[0])
		second := bytes.TrimSpace(lines[1])
		if len(first) > 1 && first[0] == '{' && first[len(first)-1] == '}' && len(second) > 0 && second[0] == '{' {
			return false
		}
	}
	return true
}
