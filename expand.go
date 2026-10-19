package abbreviatory

import (
	"bufio"
	"bytes"
	"io"
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
		return "", "", inputError("not a reference: " + quote(s) + " (expected something like 3f2a9c1b7e04:g12)")
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
			return nil, inputError("line range " + sel + " is outside the input, which has " + plural(ix.Lines, "line", "lines"))
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
				return nil, inputError("bad JSON pointer " + quote(sel) + ": " + err.Error())
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
			return nil, inputError("no group " + sel + "; this input has " + plural(int64(len(ix.Groups)), "group", "groups"))
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
		return nil, inputError("unknown selector " + quote(sel) + "; use " + selectors)
	}
	if ix.Unit == "bytes" {
		return extractBytes(original, rs), nil
	}
	return extractLines(original, rs), nil
}

// ExpandTo writes the part of an original that a selector names to w, as
// Expand returns it, reading the original as a stream: memory doesn't grow
// with the original, except for a JSON pointer or the errors of a JSON
// document, which need the document whole. A bad selector is reported
// before anything is written.
func ExpandTo(w io.Writer, original io.Reader, ix *Index, sel string) error {
	if sel == "" {
		_, err := io.Copy(w, original)
		return err
	}
	if !lineSel.MatchString(sel) && ix.Format == "json" && (sel == "errors" || strings.HasPrefix(sel, "/")) {
		data, err := io.ReadAll(original)
		if err != nil {
			return err
		}
		part, err := Expand(data, ix, sel)
		if err != nil {
			return err
		}
		_, err = w.Write(part)
		return err
	}
	rs, unit, err := selectRanges(ix, sel)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	br := bufio.NewReaderSize(original, 64<<10)
	if unit == "bytes" {
		var pos int64
		for _, r := range rs {
			if r.Start < pos || r.End < r.Start {
				continue
			}
			if _, err := io.CopyN(io.Discard, br, r.Start-pos); err != nil {
				break
			}
			n, err := io.CopyN(bw, br, r.End-r.Start+1)
			pos = r.Start + n
			if err != nil {
				break
			}
			bw.WriteByte('\n')
		}
		return bw.Flush()
	}
	line := int64(1)
	ri := 0
	for ri < len(rs) {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			if line >= rs[ri].Start {
				if _, werr := bw.Write(chunk); werr != nil {
					return werr
				}
			}
			if err == nil {
				if line >= rs[ri].End {
					ri++
				}
				line++
			}
		}
		if err == bufio.ErrBufferFull {
			continue // a long line: the rest of it comes next
		}
		if err != nil {
			break
		}
	}
	return bw.Flush()
}

// selectRanges resolves a line, group or kind selector to sorted, merged
// ranges and their unit.
func selectRanges(ix *Index, sel string) ([]Range, string, error) {
	if m := lineSel.FindStringSubmatch(sel); m != nil {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b := a
		if m[2] != "" {
			b, _ = strconv.ParseInt(m[2], 10, 64)
		}
		if a < 1 || b < a || b > ix.Lines {
			return nil, "", inputError("line range " + sel + " is outside the input, which has " + plural(ix.Lines, "line", "lines"))
		}
		return []Range{{a, b}}, "lines", nil
	}
	var rs []Range
	if m := groupSel.FindStringSubmatch(sel); m != nil {
		i, _ := strconv.Atoi(m[1])
		if i < 0 || i >= len(ix.Groups) {
			return nil, "", inputError("no group " + sel + "; this input has " + plural(int64(len(ix.Groups)), "group", "groups"))
		}
		rs = mergeRanges(groupRanges(ix.Groups[i]))
	} else if _, ok := ParseKind(sel); ok || sel == "keep" {
		for _, g := range ix.Groups {
			if (sel == "keep" && g.Kept) || g.Kind == sel {
				rs = append(rs, groupRanges(g)...)
			}
		}
		rs = mergeRanges(rs)
	} else {
		return nil, "", inputError("unknown selector " + quote(sel) + "; use " + selectors)
	}
	unit := "lines"
	if ix.Unit == "bytes" {
		unit = "bytes"
	}
	return rs, unit, nil
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
