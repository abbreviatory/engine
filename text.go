package abbreviatory

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxParseLine is the longest line adapters look at. Longer lines are cut for
// parsing and display; the stored original keeps them whole.
const maxParseLine = 64 << 10

// maxHead is the longest head or detail line shown; longer ones are cut with
// a note of how much is left out.
const maxHead = 300

// clean prepares a raw line for parsing and display: it drops a trailing
// carriage return, removes ANSI escape sequences (colours, cursor moves) and
// replaces other control characters and invalid UTF-8 with '?'.
func clean(raw []byte) string {
	if n := len(raw); n > 0 && raw[n-1] == '\n' {
		raw = raw[:n-1]
	}
	if n := len(raw); n > 0 && raw[n-1] == '\r' {
		raw = raw[:n-1]
	}
	if len(raw) > maxParseLine {
		raw = raw[:maxParseLine]
	}
	plain := true
	for _, c := range raw {
		if c < 0x20 && c != '\t' || c == 0x7f || c >= 0x80 {
			plain = false
			break
		}
	}
	if plain {
		return string(raw)
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); {
		c := raw[i]
		if c == 0x1b {
			i = skipEscape(raw, i)
			continue
		}
		if c < 0x80 {
			if c < 0x20 && c != '\t' || c == 0x7f {
				b.WriteByte('?')
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size <= 1 {
			b.WriteByte('?')
			i++
			continue
		}
		b.Write(raw[i : i+size])
		i += size
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence starting at i.
func skipEscape(b []byte, i int) int {
	i++ // ESC
	if i >= len(b) {
		return i
	}
	switch b[i] {
	case '[': // CSI: parameters, then a final byte in 0x40..0x7e
		i++
		for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
			i++
		}
		if i < len(b) {
			i++
		}
	case ']': // OSC: up to BEL or ESC backslash
		i++
		for i < len(b) {
			if b[i] == 0x07 {
				i++
				break
			}
			if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '\\' {
				i += 2
				break
			}
			i++
		}
	default:
		i++
	}
	return i
}

// cut shortens s to at most n bytes on a rune boundary, noting what is left
// out.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	k := n
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k] + "... (+" + strconv.Itoa(len(s)-k) + " chars)"
}

// group3 formats n with thousands separators: 1234567 -> "1,234,567".
func group3(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// humanBytes formats a byte count in decimal units: 412.0 kB, 8.2 MB.
func humanBytes(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1000*1000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + " kB"
	case n < 1000*1000*1000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + " MB"
	default:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + " GB"
	}
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return group3(n) + " " + many
}
