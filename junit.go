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
	// Control characters are not allowed in XML, but test runners that
	// print colours (Jest) put them in reports anyway. Read a copy with
	// them replaced; it has the same length, so offsets still match.
	src := data
	for i, c := range data {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			if &src[0] == &data[0] {
				src = append([]byte(nil), data...)
			}
			src[i] = '?'
		}
	}
	d := xml.NewDecoder(bytes.NewReader(src))
	d.Strict = false

	var cur *junitCase
	var suites []string
	var root, sum [4]int64 // tests, failures, errors, skipped
	haveRoot, haveSum := false, false
	emitted := 0
	counts := func(t xml.StartElement) ([4]int64, bool) {
		v := attr(t, "tests")
		if v == "" {
			return [4]int64{}, false
		}
		var c [4]int64
		for i, a := range []string{"tests", "failures", "errors", "skipped"} {
			c[i], _ = strconv.ParseInt(attr(t, a), 10, 64)
		}
		return c, true
	}

	for {
		off := d.InputOffset()
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if emitted == 0 && cur == nil {
				return nil, errors.New("JUnit XML: " + err.Error())
			}
			// A report cut off part way, the usual end of a killed run:
			// keep what was read, and report the unfinished testcase.
			if cur != nil {
				emit(Record{Kind: KindFail, ID: cur.id(), Head: cur.id() + " (the report is cut off inside this testcase)",
					Detail: trimDetail(splitDetail(cur.detail.String())), Lines: []Range{{cur.start, int64(len(data)) - 1}}})
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "testsuites", "testsuite":
				if c, ok := counts(t); ok {
					if t.Name.Local == "testsuites" {
						root, haveRoot = c, true
					} else if len(suites) == 0 {
						// Top-level suites: add them up.
						for i := range sum {
							sum[i] += c[i]
						}
						haveSum = true
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
				cur = &junitCase{name: attr(t, "name"), class: attr(t, "classname"), time: attr(t, "time"), kind: KindPass, start: start}
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
				id := cur.id()
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
				emitted++
				cur = nil
			case "testsuite":
				if len(suites) > 0 {
					suites = suites[:len(suites)-1]
				}
			}
		}
	}
	c := root
	if !haveRoot {
		if !haveSum {
			return nil, nil
		}
		c = sum
	}
	s := "JUnit report: " + group3(c[0]) + " tests, " + group3(c[1]) + " failures, " + group3(c[2]) + " errors, " + group3(c[3]) + " skipped"
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

type junitCase struct {
	name, class, time string
	kind              Kind
	msg               string
	detail            strings.Builder
	start             int64
	inResult          bool
}

func (c *junitCase) id() string {
	if c.class != "" {
		return c.class + "." + c.name
	}
	return c.name
}
