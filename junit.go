package abbreviatory

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// parseJUnit reads a JUnit XML report (the format most CI tools, Jest,
// Maven, Gradle, pytest --junitxml and go-junit-report write). Each
// testcase becomes a record covering its lines in the file.
func parseJUnit(data []byte, emit func(Record)) ([]string, error) {
	// Records cover byte ranges, not lines: many JUnit writers put the
	// whole report on one line.
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false

	type tc struct {
		name, class, time string
		kind              Kind
		msg               string
		detail            strings.Builder
		start             int64
		inResult          bool
	}
	var cur *tc
	var suites []string
	var tests, failures, errs, skipped int64
	haveCounts := false

	for {
		off := d.InputOffset()
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			var se *xml.SyntaxError
			if errors.As(err, &se) && cur == nil && tests+failures > 0 {
				// A report cut off after the last full testcase: keep
				// what was read.
				break
			}
			return nil, errors.New("JUnit XML: " + err.Error())
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuites", "testsuite":
				if t.Name.Local == "testsuites" || len(suites) == 0 {
					if v := attr(t, "tests"); v != "" && (t.Name.Local == "testsuites" || !haveCounts) {
						haveCounts = true
						tests, _ = strconv.ParseInt(v, 10, 64)
						failures, _ = strconv.ParseInt(attr(t, "failures"), 10, 64)
						errs, _ = strconv.ParseInt(attr(t, "errors"), 10, 64)
						skipped, _ = strconv.ParseInt(attr(t, "skipped"), 10, 64)
					}
				}
				if t.Name.Local == "testsuite" {
					suites = append(suites, attr(t, "name"))
				}
			case "testcase":
				start := off
				if i := bytes.IndexByte(data[start:], '<'); i >= 0 {
					start += int64(i)
				}
				cur = &tc{name: attr(t, "name"), class: attr(t, "classname"), time: attr(t, "time"), kind: KindPass, start: start}
			case "failure", "error", "skipped":
				if cur == nil {
					continue
				}
				switch t.Name.Local {
				case "failure":
					cur.kind = KindFail
				case "error":
					if cur.kind != KindFail {
						cur.kind = KindError
					}
				default:
					if cur.kind == KindPass {
						cur.kind = KindSkip
					}
				}
				if cur.msg == "" {
					cur.msg = attr(t, "message")
				}
				cur.inResult = true
			}
		case xml.CharData:
			if cur != nil && cur.inResult && cur.detail.Len() < 64<<10 {
				cur.detail.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "failure", "error", "skipped":
				if cur != nil {
					cur.inResult = false
				}
			case "testcase":
				if cur == nil {
					continue
				}
				end := d.InputOffset() - 1
				id := cur.name
				if cur.class != "" {
					id = cur.class + "." + cur.name
				}
				head := id
				if cur.time != "" {
					head += " (" + cur.time + "s)"
				}
				if cur.msg != "" && cur.kind != KindPass {
					head += ": " + firstTextLine(cur.msg)
				}
				r := Record{Kind: cur.kind, ID: id, Head: head, Lines: []Range{{cur.start, end}}}
				if cur.kind == KindFail || cur.kind == KindError {
					r.Detail = trimDetail(splitDetail(cur.detail.String()))
				}
				emit(r)
				cur = nil
			case "testsuite":
				if len(suites) > 0 {
					suites = suites[:len(suites)-1]
				}
			}
		}
	}
	if !haveCounts {
		return nil, nil
	}
	s := "JUnit report: " + group3(tests) + " tests, " + group3(failures) + " failures, " + group3(errs) + " errors, " + group3(skipped) + " skipped"
	return []string{s}, nil
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func firstTextLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i]) + " ..."
	}
	return s
}

func splitDetail(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		out = append(out, clean([]byte(strings.TrimRight(l, " \t"))))
		if len(out) > maxDetail {
			break
		}
	}
	return out
}
