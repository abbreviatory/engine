package abbreviatory

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ParseRef splits a reference such as "3f2a9c1b7e04:g12" into the stored
// input's ID and the selector after the colon (empty for the whole input).
// The brackets and "ref " prefix of the text form are accepted too.
func ParseRef(s string) (id, sel string, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	s = strings.TrimPrefix(s, "ref ")
	s = strings.TrimSpace(s)
	id, sel, _ = strings.Cut(s, ":")
	if !refIDPattern.MatchString(id) {
		return "", "", errors.New("not a reference: " + quote(s) + " (expected something like 3f2a9c1b7e04:g12)")
	}
	return id, sel, nil
}

var (
	refIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)
	lineSel      = regexp.MustCompile(`^L(\d+)(?:-(\d+))?$`)
	groupSel     = regexp.MustCompile(`^g(\d+)$`)
	selectors    = "the whole input (no selector), Ln or Ln-m (lines), gN (a group), fail, error, warn, info, pass, skip, keep (all groups of that kind), or for JSON a pointer such as /items/3 and errors"
)

// Expand returns the part of a stored original that a selector names. Lines
// come back exactly as they were in the input, newlines included, in their
// original order.
func Expand(original []byte, ix *Index, sel string) ([]byte, error) {
	if sel == "" {
		return original, nil
	}
	if m := lineSel.FindStringSubmatch(sel); m != nil {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b := a
		if m[2] != "" {
			b, _ = strconv.ParseInt(m[2], 10, 64)
		}
		if a < 1 || b < a || b > ix.Lines {
			return nil, errors.New("line range " + sel + " is outside the input, which has " + plural(ix.Lines, "line", "lines"))
		}
		return extractLines(original, []Range{{a, b}}), nil
	}
	if ix.Format == "json" {
		doc, err := parseJSONDoc(original)
		if err != nil {
			return nil, err
		}
		if sel == "errors" {
			return doc.errorLines(), nil
		}
		if strings.HasPrefix(sel, "/") {
			ptr, err := url.PathUnescape(sel)
			if err != nil {
				return nil, errors.New("bad JSON pointer " + quote(sel) + ": " + err.Error())
			}
			n, err := doc.lookup(ptr)
			if err != nil {
				return nil, err
			}
			return append(original[n.start:n.end:n.end], '\n'), nil
		}
	}
	var rs []Range
	if m := groupSel.FindStringSubmatch(sel); m != nil {
		i, _ := strconv.Atoi(m[1])
		if i < 0 || i >= len(ix.Groups) {
			return nil, errors.New("no group " + sel + "; this input has " + plural(int64(len(ix.Groups)), "group", "groups"))
		}
		rs = groupRanges(ix.Groups[i])
	} else if _, ok := ParseKind(sel); ok || sel == "keep" {
		for _, g := range ix.Groups {
			if (sel == "keep" && g.Kept) || g.Kind == sel {
				rs = append(rs, groupRanges(g)...)
			}
		}
		rs = mergeRanges(rs)
	} else {
		return nil, errors.New("unknown selector " + quote(sel) + "; use " + selectors)
	}
	if ix.Unit == "bytes" {
		return extractBytes(original, rs), nil
	}
	return extractLines(original, rs), nil
}

// extractBytes copies byte ranges (inclusive) out of data, one per line.
func extractBytes(data []byte, rs []Range) []byte {
	var out bytes.Buffer
	for _, r := range rs {
		if r.Start < 0 || r.End >= int64(len(data)) || r.End < r.Start {
			continue
		}
		out.Write(data[r.Start : r.End+1])
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func groupRanges(g IndexGroup) []Range {
	rs := make([]Range, len(g.Ranges))
	for i, p := range g.Ranges {
		rs[i] = Range{p[0], p[1]}
	}
	return rs
}

// extractLines copies the given sorted, merged line ranges out of data.
func extractLines(data []byte, rs []Range) []byte {
	var out bytes.Buffer
	line := int64(1)
	pos := 0
	for _, r := range rs {
		for line < r.Start && pos < len(data) {
			i := bytes.IndexByte(data[pos:], '\n')
			if i < 0 {
				pos = len(data)
				break
			}
			pos += i + 1
			line++
		}
		for line <= r.End && pos < len(data) {
			i := bytes.IndexByte(data[pos:], '\n')
			end := len(data)
			if i >= 0 {
				end = pos + i + 1
			}
			out.Write(data[pos:end])
			pos = end
			line++
		}
	}
	return out.Bytes()
}
