package abbreviatory

import (
	"hash/fnv"
	"strings"
)

// Kind classifies a record. The order matters: higher kinds are more
// important and are shown first.
type Kind uint8

const (
	KindInfo Kind = iota
	KindPass
	KindSkip
	KindWarn
	KindError
	KindFail
)

var kindNames = [...]string{"info", "pass", "skip", "warn", "error", "fail"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "info"
}

// ParseKind returns the kind with the given name.
func ParseKind(s string) (Kind, bool) {
	for i, n := range kindNames {
		if n == s {
			return Kind(i), true
		}
	}
	return KindInfo, false
}

// Range is an inclusive range of 1-based line numbers in the original input.
type Range struct {
	Start, End int64
}

// Record is one unit of output an adapter recognised: a log line with its
// stack trace, a test result with its failure message, and so on.
type Record struct {
	Kind Kind
	// ID is a stable identity such as a test name. Records with different IDs
	// never group together.
	ID string
	// Head is the one-line text shown for the record.
	Head string
	// Detail holds further lines shown when there is room (a stack trace, a
	// failure message).
	Detail []string
	// Lines are the original lines the record came from.
	Lines []Range
	// Name is a test's own name, without its package, for the formats that
	// have packages (go test). Run history uses it to match a test whose
	// package wasn't printed (a run cut short) with the same test in
	// another run.
	Name string
	// NoHistory keeps a passing test out of the grouper's list for run
	// history, because its adapter reports it separately.
	NoHistory bool
	// GroupKey, when set, is the grouping identity instead of the ID, head
	// and detail: two records with the same kind and GroupKey are one group
	// (the same change in several files of a diff, the files of one type in
	// one directory of a listing).
	GroupKey string
	// GroupHead is shown for a group of more than one record instead of the
	// first record's head, such as "net/http/*.go".
	GroupHead string
	// Items, when set, are the record's identities for run history instead
	// of its group, each a JSON pointer and value joined by a zero byte: the
	// error fields of an HTTP JSON body that share lines.
	Items []string
}

func (r *Record) addLine(n int64) {
	r.Lines = addRange(r.Lines, Range{n, n})
}

// addRange appends rg to rs, merging it into the last range when they touch.
func addRange(rs []Range, rg Range) []Range {
	if n := len(rs); n > 0 {
		last := &rs[n-1]
		if rg.Start <= last.End+1 && rg.Start >= last.Start {
			if rg.End > last.End {
				last.End = rg.End
			}
			return rs
		}
	}
	return append(rs, rg)
}

// mergeRanges returns rs sorted and merged. It keeps rs as it is when it is
// already sorted, which is the usual case.
func mergeRanges(rs []Range) []Range {
	sorted := true
	for i := 1; i < len(rs); i++ {
		if rs[i].Start < rs[i-1].Start {
			sorted = false
			break
		}
	}
	if !sorted {
		cp := append([]Range(nil), rs...)
		sortRanges(cp)
		rs = cp
	}
	out := rs[:0:0]
	for _, r := range rs {
		out = addRange(out, r)
	}
	return out
}

func sortRanges(rs []Range) {
	// Insertion sort is fine: unsorted ranges come from interleaved test
	// output, where they are short and nearly sorted.
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].Start < rs[j-1].Start; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

// fingerprint is the identity used for grouping: two records with the same
// fingerprint are the same event repeated.
func fingerprint(r *Record) string {
	h := fnv.New64a()
	h.Write([]byte{byte(r.Kind)})
	if r.GroupKey != "" {
		h.Write([]byte("\x00key\x00" + r.GroupKey))
		return string(h.Sum(nil))
	}
	h.Write([]byte(r.ID))
	h.Write([]byte{0})
	h.Write([]byte(normalize(r.Head)))
	for _, d := range r.Detail {
		h.Write([]byte{'\n'})
		h.Write([]byte(normalize(d)))
	}
	var b [8]byte
	s := h.Sum64()
	for i := range b {
		b[i] = byte(s >> (8 * i))
	}
	return string(b[:])
}

// normalize replaces the parts of a line that change between repeats of the
// same event: numbers (which covers timestamps, durations, ports, IP
// addresses and counters) and long hexadecimal runs (addresses, hashes,
// request and trace IDs, UUIDs). Runs of whitespace become one space. The
// original text is never changed; this is only used for grouping.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	space := false
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			i++
			continue
		}
		space = false
		if isHexByte(c) {
			// Take the whole alphanumeric word and decide what it is.
			j := i
			digits, hex, other := 0, 0, 0
			for j < len(s) && isWordByte(s[j]) {
				switch {
				case s[j] >= '0' && s[j] <= '9':
					digits++
				case isHexByte(s[j]):
					hex++
				default:
					other++
				}
				j++
			}
			word := s[i:j]
			switch {
			case digits > 0 && other == 0 && hex == 0:
				b.WriteByte('#')
			case digits > 0 && other == 0 && len(word) >= 4:
				b.WriteByte('#')
			case other == 0 && len(word) >= 8:
				// All hex letters, such as a request ID that happens to
				// have no digits.
				b.WriteByte('#')
			case len(word) > 2 && (word[0] == '0') && (word[1] == 'x' || word[1] == 'X') && allHex(word[2:]):
				b.WriteByte('#')
			default:
				// A word with letters: keep letters, collapse digit runs.
				writeDigitsCollapsed(&b, word)
			}
			i = j
			continue
		}
		if isWordByte(c) {
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			writeDigitsCollapsed(&b, s[i:j])
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return strings.TrimRight(b.String(), " ")
}

func writeDigitsCollapsed(b *strings.Builder, w string) {
	inDigits := false
	for k := 0; k < len(w); k++ {
		if w[k] >= '0' && w[k] <= '9' {
			if !inDigits {
				b.WriteByte('#')
			}
			inDigits = true
			continue
		}
		inDigits = false
		b.WriteByte(w[k])
	}
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isWordByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func allHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}
