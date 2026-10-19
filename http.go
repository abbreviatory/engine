package abbreviatory

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// ---- HTTP responses: curl -i, curl -v, redirect chains ----
//
// Status lines, the headers that matter (content type and length, location,
// retry and auth hints) and curl's own errors are records of their own; other
// headers and curl's connection notes fold into one group each. A JSON body
// is parsed: its error fields are kept like errors, and the rest of the body
// is one record whose detail is an outline. Any other body is read like a
// log.

var (
	statusRe = regexp.MustCompile(`^(?:< )?HTTP/\d(?:\.\d)? ([1-5]\d\d)(?: (.*))?$`)
	headerRe = regexp.MustCompile(`^([!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+):\s*(.*)$`)
	// curl -v's notes that can turn up after the body has started.
	curlNote = regexp.MustCompile(`^\* (?:Connection #|Closing connection|Connected to|  Trying|HTTP|TLS|SSL|ALPN|Request completely sent|Issue another request|Ignoring the response-body|Leftovers after chunking|Mark bundle|Re-using|Found bundle|Connection died|Recv failure|Send failure|transfer closed|OpenSSL|Failed|Couldn't|Could not)`)
	reqLine  = regexp.MustCompile(`^> [A-Z]+ \S+ HTTP/[\d.]+$`)
	curlErr  = regexp.MustCompile(`(?i)^(?:curl: \(\d+\)|\* (?:.*\b(?:could not|couldn't|failed|failure|error|timed out|refused|unable|reset|not resolve)\b))`)
)

// keyHeaders are shown on their own; the rest fold into one group.
var keyHeaders = map[string]bool{
	"content-type": true, "content-length": true, "location": true, "retry-after": true,
	"www-authenticate": true, "proxy-authenticate": true, "transfer-encoding": true,
	"x-request-id": true, "x-ratelimit-remaining": true, "warning": true,
}

const (
	httpNone = iota
	httpHeaders
	httpBody
)

type httpResp struct {
	code   int
	reason string
	ctype  string
	length string
	body   int64 // body lines
	bytes  int64
}

type httpAdapter struct {
	state   int
	verbose bool // curl -v: "*", ">" and "<" lines
	resps   []httpResp
	errs    int

	body     *logs // a non-JSON body, read like a log
	jsonBody bool
	buf      []string // a JSON body's lines, until it ends
	bufFrom  int64
	bufSize  int
	blank    bool // the line before was blank
}

func newHTTP() *httpAdapter { return &httpAdapter{} }

func (a *httpAdapter) cur() *httpResp {
	if len(a.resps) == 0 {
		a.resps = append(a.resps, httpResp{})
	}
	return &a.resps[len(a.resps)-1]
}

func (a *httpAdapter) note(n int64, text, key, head string, emit func(Record)) {
	emit(Record{Kind: KindInfo, Head: strings.TrimRight(text, " \t"), GroupKey: key, GroupHead: head, Lines: []Range{{n, n}}})
}

func (a *httpAdapter) line(n int64, text string, emit func(Record)) {
	t := strings.TrimRight(text, " \t")
	prevBlank := a.blank
	a.blank = strings.TrimPrefix(t, "<") == ""
	// In a body, a status line starts a new response only after a blank
	// line or one of curl's notes, as it would in a redirect chain.
	if m := statusRe.FindStringSubmatch(t); m != nil && (a.state != httpBody || prevBlank || strings.HasPrefix(t, "< ")) {
		a.endBody(emit)
		code, _ := strconv.Atoi(m[1])
		a.resps = append(a.resps, httpResp{code: code, reason: m[2]})
		a.state = httpHeaders
		kind := KindInfo
		if code >= 400 {
			kind = KindError
			a.errs++
		}
		head := strings.TrimPrefix(t, "< ")
		// Known by its code and reason, not by where it is in a chain.
		emit(Record{Kind: kind, Head: head, GroupKey: "status\x00" + m[1] + " " + m[2], GroupHead: head, Lines: []Range{{n, n}}})
		return
	}
	if strings.HasPrefix(t, "curl: (") {
		// curl's own error, on stderr, often after the body.
		emit(Record{Kind: KindError, Head: t, Lines: []Range{{n, n}}})
		return
	}
	if a.verbose && a.state == httpBody {
		// Inside a body only curl's own notes are taken out of it.
		if reqLine.MatchString(t) {
			// curl -L -v: the next request of a redirect chain.
			a.endBody(emit)
			f := strings.Fields(t)
			emit(Record{Kind: KindInfo, ID: "request", Head: "request: " + f[1] + " " + f[2], Lines: []Range{{n, n}}})
			return
		}
		if strings.HasPrefix(t, "{ [") || strings.HasPrefix(t, "} [") || curlNote.MatchString(t) {
			if curlErr.MatchString(t) {
				emit(Record{Kind: KindError, Head: t, Lines: []Range{{n, n}}})
				return
			}
			a.note(n, t, "curl", "(curl connection notes)", emit)
			a.blank = prevBlank
			return
		}
	} else if a.verbose || a.state == httpNone {
		switch {
		case strings.HasPrefix(t, "* ") || t == "*":
			a.verbose = true
			if curlErr.MatchString(t) {
				emit(Record{Kind: KindError, Head: t, Lines: []Range{{n, n}}})
				return
			}
			a.note(n, t, "curl", "(curl connection notes)", emit)
			return
		case strings.HasPrefix(t, "> ") || t == ">":
			a.verbose = true
			if m := strings.Fields(t[min(2, len(t)):]); len(m) == 3 && strings.HasPrefix(m[2], "HTTP/") {
				a.endBody(emit)
				emit(Record{Kind: KindInfo, ID: "request", Head: "request: " + m[0] + " " + m[1], Lines: []Range{{n, n}}})
				a.state = httpNone
				return
			}
			a.note(n, t, "req", "(request headers)", emit)
			return
		case strings.HasPrefix(t, "{ [") || strings.HasPrefix(t, "} ["):
			a.note(n, t, "curl", "(curl connection notes)", emit)
			return
		}
	}
	switch a.state {
	case httpHeaders:
		h := strings.TrimPrefix(t, "<")
		h = strings.TrimPrefix(h, " ")
		if h == "" {
			a.note(n, t, "meta", "(blank lines)", emit)
			a.startBody()
			return
		}
		if m := headerRe.FindStringSubmatch(h); m != nil {
			name := strings.ToLower(m[1])
			r := a.cur()
			switch name {
			case "content-type":
				r.ctype = m[2]
			case "content-length":
				r.length = m[2]
			}
			if keyHeaders[name] {
				kind := KindInfo
				if name == "warning" {
					kind = KindWarn
				}
				emit(Record{Kind: kind, ID: "header:" + strconv.Itoa(len(a.resps)) + ":" + name, Head: h, Lines: []Range{{n, n}}})
				return
			}
			a.note(n, h, "hdr", "(other headers)", emit)
			return
		}
		// Not a header after all: the body starts without a blank line.
		a.startBody()
		a.bodyLine(n, text, emit)
	case httpBody:
		a.bodyLine(n, text, emit)
	default:
		if t == "" {
			a.note(n, t, "meta", "(blank lines)", emit)
			return
		}
		a.startBody()
		a.bodyLine(n, text, emit)
	}
}

func (a *httpAdapter) startBody() {
	a.state = httpBody
	r := a.cur()
	a.jsonBody = strings.Contains(strings.ToLower(r.ctype), "json")
	a.body = nil
	a.buf, a.bufSize = nil, 0
}

func (a *httpAdapter) bodyLine(n int64, text string, emit func(Record)) {
	r := a.cur()
	r.body++
	r.bytes += int64(len(text)) + 1
	if a.jsonBody {
		if len(a.buf) == 0 {
			a.bufFrom = n
		}
		a.buf = append(a.buf, text)
		a.bufSize += len(text) + 1
		if a.bufSize <= maxJSON {
			return
		}
		// Too big to parse whole: read what's buffered as a log.
		a.jsonBody = false
		a.replay(emit)
		return
	}
	if a.body == nil {
		a.body = newLogs()
	}
	a.body.line(n, text, emit)
}

// replay reads the buffered body lines as a log.
func (a *httpAdapter) replay(emit func(Record)) {
	if a.body == nil {
		a.body = newLogs()
	}
	for i, l := range a.buf {
		a.body.line(a.bufFrom+int64(i), l, emit)
	}
	a.buf, a.bufSize = nil, 0
}

func (a *httpAdapter) endBody(emit func(Record)) {
	if a.jsonBody && len(a.buf) > 0 {
		if !a.jsonRecords(emit) {
			a.replay(emit)
		}
	}
	a.buf, a.bufSize = nil, 0
	if a.body != nil {
		a.body.finish(emit)
		a.body = nil
	}
	a.jsonBody = false
	a.state = httpNone
}

// jsonRecords parses a buffered JSON body. Error fields become error
// records, error fields that share lines one record; the other lines of the
// body are one record with the document's outline as its detail.
func (a *httpAdapter) jsonRecords(emit func(Record)) bool {
	// Trailing blank lines aren't part of the document.
	end := len(a.buf)
	for end > 0 && strings.TrimSpace(a.buf[end-1]) == "" {
		end--
	}
	if end == 0 {
		return false
	}
	data := []byte(strings.Join(a.buf[:end], "\n"))
	doc, err := parseJSONDoc(data)
	if err != nil {
		return false
	}
	// Line starts, to turn offsets into line numbers.
	starts := make([]int, 0, end)
	off := 0
	for _, l := range a.buf[:end] {
		starts = append(starts, off)
		off += len(l) + 1
	}
	lineOf := func(o int) int64 {
		lo, hi := 0, len(starts)-1
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if starts[mid] <= o {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return a.bufFrom + int64(lo)
	}
	type cluster struct {
		from, to int64
		ptrs     []string
		heads    []string
		items    []string
	}
	var cs []cluster
	for _, p := range doc.errors {
		nd, err := doc.lookup(p)
		if err != nil {
			continue
		}
		from, to := lineOf(nd.start), lineOf(max(nd.start, nd.end-1))
		val := strings.Join(strings.Fields(string(data[nd.start:nd.end])), " ")
		head := "body " + p + ": " + cut(val, 160)
		if k := len(cs) - 1; k >= 0 && from <= cs[k].to {
			cs[k].to = max(cs[k].to, to)
			cs[k].ptrs = append(cs[k].ptrs, p)
			cs[k].heads = append(cs[k].heads, head)
			cs[k].items = append(cs[k].items, p+"\x00"+val)
			continue
		}
		cs = append(cs, cluster{from: from, to: to, ptrs: []string{p}, heads: []string{head}, items: []string{p + "\x00" + val}})
	}
	// The body's other lines.
	var rest []Range
	next := a.bufFrom
	for _, c := range cs {
		if c.from > next {
			rest = append(rest, Range{next, c.from - 1})
		}
		next = c.to + 1
	}
	last := a.bufFrom + int64(len(a.buf)) - 1
	if next <= last {
		rest = append(rest, Range{next, last})
	}
	for _, c := range cs {
		// Grouped by its fields' pointers (array positions aside) and values;
		// history knows each field on its own.
		var key []string
		for _, it := range c.items {
			p, v, _ := strings.Cut(it, "\x00")
			key = append(key, normalize(p)+"\x00"+normalize(cut(v, maxHead)))
		}
		r := Record{Kind: KindError, Head: c.heads[0], GroupKey: "jsonerr\x00" + strings.Join(key, "\x01"), Items: c.items, Lines: []Range{{c.from, c.to}}}
		if len(c.heads) > 1 {
			r.Head += " (and " + plural(int64(len(c.heads)-1), "more error field", "more error fields") + ")"
			for _, h := range c.heads[1:] {
				if len(r.Detail) == maxDetail {
					break
				}
				r.Detail = append(r.Detail, h)
			}
		}
		emit(r)
	}
	if len(rest) > 0 {
		head := "body: JSON " + describe(doc.root) + ", " + plural(doc.values, "value", "values")
		if len(doc.errors) > 0 {
			head += ", " + plural(int64(len(doc.errors)), "error field", "error fields")
		}
		emit(Record{Kind: KindInfo, ID: "body:" + strconv.Itoa(len(a.resps)), Head: head, Detail: jsonOutline(doc), Lines: rest})
	}
	return true
}

// jsonOutline is the document's outline at the first level that fits in
// maxDetail lines.
func jsonOutline(doc *jsonDoc) []string {
	errSet := map[string]bool{}
	for _, e := range doc.errors {
		errSet[e] = true
	}
	var out []string
	for _, lv := range jlevels {
		r := &jrender{lv: lv, ref: func(string) string { return "" }, shown: map[string]bool{}, errSet: errSet}
		r.node(doc.root, "", 0, 0)
		out = strings.Split(strings.TrimLeft(r.b.String(), "\n"), "\n")
		if len(out) <= 60 {
			break
		}
	}
	if len(out) > maxDetail {
		out = out[:maxDetail]
	}
	for i, l := range out {
		out[i] = cut(l, maxHead)
	}
	return out
}

func (a *httpAdapter) finish(emit func(Record)) []string {
	a.endBody(emit)
	if len(a.resps) == 0 {
		return []string{"http: no status line"}
	}
	var chain []string
	for _, r := range a.resps {
		s := strconv.Itoa(r.code)
		if r.reason != "" {
			s += " " + r.reason
		}
		chain = append(chain, s)
	}
	s := "http: " + plural(int64(len(a.resps)), "response", "responses") + ": "
	if len(chain) > 6 {
		chain = append(append(chain[:3:3], "…"), chain[len(chain)-2:]...)
	}
	s += strings.Join(chain, " -> ")
	last := a.resps[len(a.resps)-1]
	if last.body > 0 || last.ctype != "" {
		s += "; body " + humanBytes(last.bytes)
		if last.ctype != "" {
			s += " " + strings.TrimSpace(strings.SplitN(last.ctype, ";", 2)[0])
		}
	}
	return []string{cut(s, maxHead)}
}

// looksHTTP reports an HTTP response as curl prints it: a status line first,
// or curl -v's "*" and ">" lines and then one.
func looksHTTP(sample []byte) bool {
	lines := bytes.SplitN(sample, []byte("\n"), 60)
	for _, l := range lines {
		t := string(bytes.TrimRight(l, "\r"))
		switch {
		case statusRe.MatchString(t):
			return true
		case t == "" || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "> ") || t == ">" || strings.HasPrefix(t, "{ [") || strings.HasPrefix(t, "} ["):
			continue
		}
		return false
	}
	return false
}
