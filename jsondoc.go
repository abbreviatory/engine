package abbreviatory

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// jnode is a parsed JSON value that remembers where it sits in the input, so
// any part of it can be expanded back byte for byte.
type jnode struct {
	kind       byte // 'o' object, 'a' array, 's' string, 'n' number, 'b' bool, 'z' null
	keys       []string
	vals       []*jnode
	str        string // decoded string, or the literal text of a number or bool
	start, end int
	hasErr     bool // an error field is at or below this node
}

type jsonDoc struct {
	root   *jnode
	data   []byte
	errors []string // JSON pointers of error fields, in document order
	values int64
}

const maxJSONDepth = 2000

func parseJSONDoc(data []byte) (*jsonDoc, error) {
	p := &jparser{data: data}
	p.skipBOM()
	p.ws()
	root, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.data) {
		return nil, p.fail("extra data after the JSON value")
	}
	doc := &jsonDoc{root: root, data: data}
	doc.findErrors(root, "")
	return doc, nil
}

type jparser struct {
	data []byte
	i    int
	n    int64
}

func (p *jparser) skipBOM() {
	if bytes.HasPrefix(p.data, []byte("\xef\xbb\xbf")) {
		p.i = 3
	}
}

func (p *jparser) fail(msg string) error {
	line := 1 + bytes.Count(p.data[:p.i], []byte{'\n'})
	return inputError("JSON: " + msg + " on line " + strconv.Itoa(line))
}

func (p *jparser) ws() {
	for p.i < len(p.data) {
		switch p.data[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jparser) value(depth int) (*jnode, error) {
	if depth > maxJSONDepth {
		return nil, p.fail("nested too deeply")
	}
	if p.i >= len(p.data) {
		return nil, p.fail("unexpected end of input")
	}
	start := p.i
	switch c := p.data[p.i]; {
	case c == '{':
		n := &jnode{kind: 'o', start: start}
		p.i++
		p.ws()
		if p.i < len(p.data) && p.data[p.i] == '}' {
			p.i++
			n.end = p.i
			return n, nil
		}
		for {
			p.ws()
			if p.i >= len(p.data) || p.data[p.i] != '"' {
				return nil, p.fail("expected a key")
			}
			k, err := p.str()
			if err != nil {
				return nil, err
			}
			p.ws()
			if p.i >= len(p.data) || p.data[p.i] != ':' {
				return nil, p.fail("expected ':'")
			}
			p.i++
			p.ws()
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, k)
			n.vals = append(n.vals, v)
			p.ws()
			if p.i < len(p.data) && p.data[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.data) && p.data[p.i] == '}' {
				p.i++
				n.end = p.i
				return n, nil
			}
			return nil, p.fail("expected ',' or '}'")
		}
	case c == '[':
		n := &jnode{kind: 'a', start: start}
		p.i++
		p.ws()
		if p.i < len(p.data) && p.data[p.i] == ']' {
			p.i++
			n.end = p.i
			return n, nil
		}
		for {
			p.ws()
			v, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.vals = append(n.vals, v)
			p.ws()
			if p.i < len(p.data) && p.data[p.i] == ',' {
				p.i++
				continue
			}
			if p.i < len(p.data) && p.data[p.i] == ']' {
				p.i++
				n.end = p.i
				return n, nil
			}
			return nil, p.fail("expected ',' or ']'")
		}
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		p.n++
		return &jnode{kind: 's', str: s, start: start, end: p.i}, nil
	case c == 't' || c == 'f' || c == 'n':
		for _, lit := range []string{"true", "false", "null"} {
			if bytes.HasPrefix(p.data[p.i:], []byte(lit)) {
				p.i += len(lit)
				k := byte('b')
				if lit == "null" {
					k = 'z'
				}
				return &jnode{kind: k, str: lit, start: start, end: p.i}, nil
			}
		}
		return nil, p.fail("unknown literal")
	case c == '-' || (c >= '0' && c <= '9'):
		j := p.i
		for j < len(p.data) && strings.IndexByte("+-0123456789.eE", p.data[j]) >= 0 {
			j++
		}
		lit := p.data[p.i:j]
		if !json.Valid(lit) {
			return nil, p.fail("bad number")
		}
		p.i = j
		return &jnode{kind: 'n', str: string(lit), start: start, end: p.i}, nil
	default:
		return nil, p.fail("unexpected character " + strconv.QuoteRune(rune(c)))
	}
}

func (p *jparser) str() (string, error) {
	j := p.i + 1
	simple := true
	for j < len(p.data) {
		c := p.data[j]
		if c == '"' {
			break
		}
		if c == '\\' {
			simple = false
			j += 2
			continue
		}
		if c < 0x20 {
			return "", p.fail("control character in string")
		}
		if c >= 0x80 {
			simple = false
		}
		j++
	}
	if j >= len(p.data) {
		return "", p.fail("unterminated string")
	}
	raw := p.data[p.i : j+1]
	p.i = j + 1
	if simple {
		return string(raw[1 : len(raw)-1]), nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", p.fail("bad string")
	}
	return s, nil
}

var errorKeys = map[string]bool{
	"error": true, "errors": true, "err": true, "exception": true, "exceptions": true,
	"fault": true, "faults": true, "failure": true, "failures": true,
	"stacktrace": true, "stack_trace": true, "traceback": true, "error_message": true, "errormessage": true,
}

var errorStates = map[string]bool{"error": true, "fatal": true, "failed": true, "failure": true, "critical": true, "crit": true}

// findErrors marks error fields: keys named like error with a non-empty
// value, and objects whose level, severity or status says error.
func (d *jsonDoc) findErrors(n *jnode, ptr string) bool {
	d.values++
	found := false
	switch n.kind {
	case 'o':
		for i, k := range n.keys {
			v := n.vals[i]
			child := ptr + "/" + escapePointer(k)
			lk := strings.ToLower(k)
			if errorKeys[lk] && !emptyJSON(v) {
				d.errors = append(d.errors, child)
				v.hasErr = true
				found = true
			} else if (lk == "level" || lk == "severity" || lk == "status" || lk == "state" || lk == "outcome") && v.kind == 's' && errorStates[strings.ToLower(v.str)] {
				d.errors = append(d.errors, child)
				v.hasErr = true
				found = true
			}
			if d.findErrors(v, child) {
				found = true
			}
		}
	case 'a':
		for i, v := range n.vals {
			if d.findErrors(v, ptr+"/"+strconv.Itoa(i)) {
				found = true
			}
		}
	}
	if found {
		n.hasErr = true
	}
	return found
}

func emptyJSON(n *jnode) bool {
	switch n.kind {
	case 'z':
		return true
	case 'b':
		return n.str == "false"
	case 's':
		return n.str == ""
	case 'o':
		return len(n.keys) == 0
	case 'a':
		return len(n.vals) == 0
	case 'n':
		return n.str == "0"
	}
	return false
}

func escapePointer(k string) string {
	k = strings.ReplaceAll(k, "~", "~0")
	return strings.ReplaceAll(k, "/", "~1")
}

func unescapePointer(k string) string {
	k = strings.ReplaceAll(k, "~1", "/")
	return strings.ReplaceAll(k, "~0", "~")
}

// lookup finds the node at a JSON pointer.
func (d *jsonDoc) lookup(ptr string) (*jnode, error) {
	if ptr == "" {
		return d.root, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, inputError("a JSON pointer starts with /")
	}
	n := d.root
	for _, part := range strings.Split(ptr[1:], "/") {
		part = unescapePointer(part)
		switch n.kind {
		case 'o':
			found := false
			for i, k := range n.keys {
				if k == part {
					n = n.vals[i]
					found = true
					break
				}
			}
			if !found {
				return nil, inputError("no member " + quote(part) + " at " + ptr)
			}
		case 'a':
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(n.vals) {
				return nil, inputError("no element " + quote(part) + " at " + ptr)
			}
			n = n.vals[i]
		default:
			return nil, inputError(ptr + " goes inside a value that is not an object or array")
		}
	}
	return n, nil
}

// ---- rendering ----

type jlevel struct {
	samples, str, depth, keys, kept, over int
}

var jlevels = []jlevel{
	{samples: 3, str: 200, depth: 20, keys: 1 << 30, kept: 5, over: 8},
	{samples: 2, str: 120, depth: 10, keys: 50, kept: 3, over: 5},
	{samples: 1, str: 60, depth: 6, keys: 20, kept: 2, over: 3},
	{samples: 1, str: 40, depth: 4, keys: 10, kept: 1, over: 2},
	{samples: 0, str: 24, depth: 3, keys: 6, kept: 1, over: 1},
	{samples: 0, str: 16, depth: 2, keys: 4, kept: 0, over: 0},
	{samples: 0, str: 12, depth: 1, keys: 2, kept: 0, over: 0},
}

type jrender struct {
	b      strings.Builder
	lv     jlevel
	ref    func(string) string
	shown  map[string]bool
	errSet map[string]bool
	full   bool // the whole document fits as it is
}

func renderJSON(res *Result, doc *jsonDoc, budget int, refs bool) (string, bool, error) {
	ref := func(sel string) string {
		if !refs {
			return ""
		}
		return res.ID + ":" + refPointer(sel)
	}
	head := headerLine(res, refs) + "\n"
	shape := "json: " + describe(doc.root) + ", " + plural(doc.values, "value", "values") + ", " + plural(int64(len(doc.errors)), "error field", "error fields") + "\n"
	errSet := map[string]bool{}
	for _, e := range doc.errors {
		errSet[e] = true
	}
	footerFor := func(shown map[string]bool) string {
		missing := 0
		for _, e := range doc.errors {
			if !shown[e] {
				missing++
			}
		}
		if missing == 0 {
			return ""
		}
		s := "not shown: " + group3(int64(missing)) + " of " + group3(int64(len(doc.errors))) + " error fields"
		if refs {
			s += " [ref " + res.ID + ":errors]"
		}
		return s + "\n"
	}
	// The document as it is, when it fits.
	if pretty := compactOrPretty(doc); len(head)+len(shape)+len(pretty)+1 <= budget {
		return head + shape + pretty + "\n", false, nil
	}
	for _, lv := range jlevels {
		r := &jrender{lv: lv, ref: ref, shown: map[string]bool{}, errSet: errSet}
		r.node(doc.root, "", 0, 0)
		r.b.WriteByte('\n')
		foot := footerFor(r.shown)
		text := head + shape + r.b.String() + foot
		if len(text) <= budget {
			return text, foot != "", nil
		}
	}
	// Not even the outline fits: the counts and a reference.
	text := head + shape + "(document too large to outline in this budget)\n" + footerFor(map[string]bool{})
	if refs {
		text = head + shape + "(document too large to outline in this budget; whole document: [ref " + res.ID + "])\n" + footerFor(map[string]bool{})
	}
	if len(text) > budget {
		return "", false, &BudgetError{Budget: budget, Need: len(text)}
	}
	return text, len(doc.errors) > 0, nil
}

// compactOrPretty returns the document re-indented with two spaces.
func compactOrPretty(doc *jsonDoc) string {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimPrefix(doc.data, []byte("\xef\xbb\xbf")), "", "  "); err != nil {
		return string(doc.data)
	}
	return b.String()
}

func describe(n *jnode) string {
	switch n.kind {
	case 'o':
		return "object with " + plural(int64(len(n.keys)), "key", "keys")
	case 'a':
		return "array of " + group3(int64(len(n.vals)))
	case 's':
		return "string"
	case 'n':
		return "number"
	case 'b':
		return "boolean"
	}
	return "null"
}

func (r *jrender) indent(level int) {
	r.b.WriteByte('\n')
	for i := 0; i < level; i++ {
		r.b.WriteString("  ")
	}
}

func (r *jrender) mark(ptr string) {
	if r.errSet[ptr] {
		r.shown[ptr] = true
	}
}

func (r *jrender) quote(s string) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	r.b.Write(bytes.TrimRight(b.Bytes(), "\n"))
}

// node writes n at pointer ptr; depth is how deep in the document it is,
// level the indentation.
func (r *jrender) node(n *jnode, ptr string, depth, level int) {
	r.mark(ptr)
	switch n.kind {
	case 's':
		s := n.str
		if len(s) > r.lv.str {
			k := r.lv.str
			for k > 0 && !utf8.RuneStart(s[k]) {
				k--
			}
			note := "... (+" + strconv.Itoa(len(s)-k) + " chars"
			if ref := r.ref(ptr); ref != "" {
				note += ", ref " + ref
			}
			s = s[:k] + note + ")"
		}
		r.quote(s)
		return
	case 'n', 'b', 'z':
		r.b.WriteString(n.str)
		return
	}
	if depth >= r.lv.depth && !(n.hasErr && depth < r.lv.depth+2) {
		r.b.WriteString(`{"_abbreviated": `)
		r.quote(describe(n))
		if ref := r.ref(ptr); ref != "" {
			r.b.WriteString(`, "ref": `)
			r.quote(ref)
		}
		r.b.WriteString("}")
		return
	}
	if n.kind == 'o' {
		if len(n.keys) == 0 {
			r.b.WriteString("{}")
			return
		}
		r.b.WriteString("{")
		shown := 0
		for i, k := range n.keys {
			// Past the key limit, error fields and their parents still show.
			if shown >= r.lv.keys && !n.vals[i].hasErr {
				continue
			}
			if shown > 0 {
				r.b.WriteString(",")
			}
			r.indent(level + 1)
			r.quote(k)
			r.b.WriteString(": ")
			r.node(n.vals[i], ptr+"/"+escapePointer(k), depth+1, level+1)
			shown++
		}
		if rest := len(n.keys) - shown; rest > 0 {
			r.b.WriteString(",")
			r.indent(level + 1)
			r.b.WriteString(`"_abbreviated": `)
			msg := plural(int64(rest), "more key", "more keys")
			if ref := r.ref(ptr); ref != "" {
				msg += ", ref " + ref
			}
			r.quote(msg)
		}
		r.indent(level)
		r.b.WriteString("}")
		return
	}
	// Arrays.
	if len(n.vals) == 0 {
		r.b.WriteString("[]")
		return
	}
	if len(n.vals) <= r.lv.over {
		r.b.WriteString("[")
		for i, v := range n.vals {
			if i > 0 {
				r.b.WriteString(",")
			}
			r.indent(level + 1)
			r.node(v, ptr+"/"+strconv.Itoa(i), depth+1, level+1)
		}
		r.indent(level)
		r.b.WriteString("]")
		return
	}
	// A long array: its shape, a few samples, the elements with errors.
	r.b.WriteString("{")
	r.indent(level + 1)
	r.b.WriteString(`"_abbreviated": `)
	r.quote(arrayShape(n))
	if keys := commonKeys(n); len(keys) > 0 {
		r.b.WriteString(",")
		r.indent(level + 1)
		r.b.WriteString(`"keys": [`)
		for i, k := range keys {
			if i > 0 {
				r.b.WriteString(", ")
			}
			r.quote(k)
		}
		r.b.WriteString("]")
	}
	samples := r.lv.samples
	if samples > len(n.vals) {
		samples = len(n.vals)
	}
	if samples > 0 {
		r.b.WriteString(",")
		r.indent(level + 1)
		r.b.WriteString(`"first": [`)
		for i := 0; i < samples; i++ {
			if i > 0 {
				r.b.WriteString(",")
			}
			r.indent(level + 2)
			r.node(n.vals[i], ptr+"/"+strconv.Itoa(i), depth+1, level+2)
		}
		r.indent(level + 1)
		r.b.WriteString("]")
	}
	var withErr []int
	for i := samples; i < len(n.vals); i++ {
		if n.vals[i].hasErr {
			withErr = append(withErr, i)
		}
	}
	if len(withErr) > 0 && r.lv.kept > 0 {
		r.b.WriteString(",")
		r.indent(level + 1)
		r.b.WriteString(`"with_errors": [`)
		for j, i := range withErr {
			if j >= r.lv.kept {
				break
			}
			if j > 0 {
				r.b.WriteString(",")
			}
			r.indent(level + 2)
			r.b.WriteString(`{"index": ` + strconv.Itoa(i) + `, "value": `)
			r.node(n.vals[i], ptr+"/"+strconv.Itoa(i), depth+1, level+2)
			r.b.WriteString("}")
		}
		r.indent(level + 1)
		r.b.WriteString("]")
		if len(withErr) > r.lv.kept {
			r.b.WriteString(",")
			r.indent(level + 1)
			r.b.WriteString(`"more_with_errors": ` + strconv.Itoa(len(withErr)-r.lv.kept))
		}
	}
	if ref := r.ref(ptr); ref != "" {
		r.b.WriteString(",")
		r.indent(level + 1)
		r.b.WriteString(`"ref": `)
		r.quote(ref)
	}
	r.indent(level)
	r.b.WriteString("}")
}

func arrayShape(n *jnode) string {
	kinds := map[byte]int{}
	for _, v := range n.vals {
		kinds[v.kind]++
	}
	names := map[byte]string{'o': "objects", 'a': "arrays", 's': "strings", 'n': "numbers", 'b': "booleans", 'z': "nulls"}
	if len(kinds) == 1 {
		for k := range kinds {
			return "array of " + group3(int64(len(n.vals))) + " " + names[k]
		}
	}
	var parts []string
	for _, k := range []byte{'o', 'a', 's', 'n', 'b', 'z'} {
		if c := kinds[k]; c > 0 {
			parts = append(parts, group3(int64(c))+" "+names[k])
		}
	}
	return "array of " + group3(int64(len(n.vals))) + " (" + strings.Join(parts, ", ") + ")"
}

// commonKeys returns the keys of an array of objects, in first-seen order,
// when the objects mostly share them.
func commonKeys(n *jnode) []string {
	count := map[string]int{}
	var order []string
	objs := 0
	for _, v := range n.vals {
		if v.kind != 'o' {
			continue
		}
		objs++
		for _, k := range v.keys {
			if count[k] == 0 {
				order = append(order, k)
			}
			count[k]++
		}
	}
	if objs == 0 {
		return nil
	}
	var out []string
	for _, k := range order {
		if count[k]*2 >= objs {
			out = append(out, k)
		}
		if len(out) == 30 {
			break
		}
	}
	return out
}

// errorLines returns "pointer<TAB>value" for every error field, the value
// as it appears in the input.
func (d *jsonDoc) errorLines() []byte {
	var b bytes.Buffer
	for _, p := range d.errors {
		n, err := d.lookup(p)
		if err != nil {
			continue
		}
		b.WriteString(p)
		b.WriteByte('\t')
		b.Write(d.data[n.start:n.end])
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// refPointer writes a JSON pointer for a reference: each segment is
// percent-encoded, so keys with spaces, quotes or other awkward characters
// still give a reference that can be copied and expanded.
func refPointer(ptr string) string {
	if ptr == "" {
		return ""
	}
	parts := strings.Split(ptr, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
