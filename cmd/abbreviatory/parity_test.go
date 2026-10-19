package main

// The 0.3 acceptance test: the command line, the HTTP service and the MCP
// server give the same results on the whole corpus. Each front end gets a
// store of its own; every fixture is reduced with several option sets,
// every reference in every report is expanded, and run histories built
// through each are compared.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// option sets, as command line flags; the HTTP and MCP forms are derived.
type optSet struct {
	name     string
	format   string
	budget   int
	preserve *string
	keep     []string
	noStore  bool
}

func strp(s string) *string { return &s }

var optSets = []optSet{
	{name: "defaults"},
	{name: "small", budget: 1024},
	{name: "failures and a keep rule", budget: 2048, preserve: strp("failures"), keep: []string{`queue depth=3[0-9]|test_parse`}},
	{name: "read as a log", format: "logs", budget: 3000},
	{name: "nothing preserved, no store", preserve: strp(""), noStore: true},
}

func (o optSet) flags() []string {
	var f []string
	if o.format != "" {
		f = append(f, "-format", o.format)
	}
	if o.budget != 0 {
		f = append(f, "-budget", strconv.Itoa(o.budget))
	}
	if o.preserve != nil {
		f = append(f, "-preserve", *o.preserve)
	}
	for _, k := range o.keep {
		f = append(f, "-keep", k)
	}
	if o.noStore {
		f = append(f, "-no-store")
	}
	return f
}

func (o optSet) query() url.Values {
	v := url.Values{}
	if o.format != "" {
		v.Set("format", o.format)
	}
	if o.budget != 0 {
		v.Set("budget", strconv.Itoa(o.budget))
	}
	if o.preserve != nil {
		v.Set("preserve", *o.preserve)
	}
	for _, k := range o.keep {
		v.Add("keep", k)
	}
	if o.noStore {
		v.Set("no_store", "true")
	}
	return v
}

func (o optSet) args() map[string]any {
	a := map[string]any{}
	if o.format != "" {
		a["format"] = o.format
	}
	if o.budget != 0 {
		a["budget"] = o.budget
	}
	if o.preserve != nil {
		a["preserve"] = *o.preserve
	}
	if len(o.keep) > 0 {
		a["keep"] = o.keep
	}
	if o.noStore {
		a["no_store"] = true
	}
	return a
}

// frontEnds are the three ways in, each with its own store.
type frontEnds struct {
	t                   *testing.T
	cliDir, mcpDir      string
	srv                 *httptest.Server
	mcp                 *mcpServer
	token               string
	cliRuns, mcpRunsOut bytes.Buffer
}

func newFrontEnds(t *testing.T) *frontEnds {
	f := &frontEnds{t: t, cliDir: t.TempDir(), mcpDir: t.TempDir(), token: "s3cret"}
	f.srv = httptest.NewServer(newHandler(core{t.TempDir()}, f.token, 1<<30))
	t.Cleanup(f.srv.Close)
	f.mcp = &mcpServer{c: core{f.mcpDir}, out: io.Discard, running: map[string]*mcpCall{}}
	return f
}

func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, data)
	}
	return m
}

// normal drops what may differ between front ends for good reason: the
// time a run was recorded.
func normal(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k != "time" {
				out[k] = normal(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = normal(val)
		}
		return out
	}
	return v
}

// jsonRound passes a value through JSON, so maps built in Go compare with
// maps decoded from JSON.
func jsonRound(t *testing.T, v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	json.Unmarshal(data, &out)
	return out
}

func (f *frontEnds) cliReduce(path string, extra ...string) map[string]any {
	args := append([]string{"reduce", "-store", f.cliDir, "-json"}, extra...)
	args = append(args, path)
	out, errs, code := runCLI(f.t, "", args...)
	if code != 0 {
		f.t.Fatalf("cli %v: exit %d: %s", args, code, errs)
	}
	return decode(f.t, []byte(out))
}

func (f *frontEnds) httpDo(method, path string, body io.Reader, accept string) (int, []byte) {
	req, _ := http.NewRequest(method, f.srv.URL+path, body)
	req.Header.Set("Authorization", "Bearer "+f.token)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (f *frontEnds) httpReduce(path string, q url.Values) map[string]any {
	file, err := os.Open(path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	code, data := f.httpDo("POST", "/reduce?"+q.Encode(), file, "")
	if code != 200 {
		f.t.Fatalf("http reduce %s: %d %s", q.Encode(), code, data)
	}
	return decode(f.t, data)
}

// mcpCall calls a tool and returns its result.
func (f *frontEnds) mcpCall(name string, args map[string]any) map[string]any {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	res, rerr := f.mcp.call(params, nil)
	if rerr != nil {
		f.t.Fatalf("mcp %s: %v", name, rerr.Message)
	}
	return jsonRound(f.t, res).(map[string]any)
}

func mcpText(r map[string]any) string {
	return r["content"].([]any)[0].(map[string]any)["text"].(string)
}

var anyRefRE = regexp.MustCompile(`(?:\[ref |"ref": "|, ref )([0-9a-f]{12})(?::([^\]"\s)]+))?`)

func TestFrontEndsAgree(t *testing.T) {
	paths, _ := filepath.Glob("../../test/fixtures/*")
	var fixtures []string
	for _, p := range paths {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() {
			fixtures = append(fixtures, p)
		}
	}
	sort.Strings(fixtures)
	if len(fixtures) < 8 {
		t.Fatalf("expected the fixtures, found %d", len(fixtures))
	}
	sets := optSets
	if testing.Short() || raceEnabled {
		// The race detector makes this slow: the defaults and one other set
		// still cover every fixture and every reference.
		sets = optSets[:2]
	}
	f := newFrontEnds(t)
	reductions, expansions := 0, 0
	for _, path := range fixtures {
		name := filepath.Base(path)
		for _, o := range sets {
			cli := f.cliReduce(path, o.flags()...)
			web := f.httpReduce(path, o.query())
			m := f.mcpCall("reduce", mergeArgs(o.args(), map[string]any{"path": path}))
			if m["isError"] == true {
				t.Fatalf("%s, %s: mcp: %s", name, o.name, mcpText(m))
			}
			viaMCP := m["structuredContent"].(map[string]any)
			if !reflect.DeepEqual(cli, web) || !reflect.DeepEqual(cli, viaMCP) {
				t.Fatalf("%s, %s: the front ends differ\ncli:  %v\nhttp: %v\nmcp:  %v", name, o.name, cli, web, viaMCP)
			}
			if mcpText(m) != cli["text"] {
				t.Fatalf("%s, %s: mcp text differs from the report", name, o.name)
			}
			reductions++
			if o.noStore {
				continue
			}
			// Every reference expands to the same bytes everywhere.
			text := cli["text"].(string)
			refs := anyRefRE.FindAllStringSubmatch(text, -1)
			if len(refs) == 0 {
				t.Fatalf("%s, %s: no references in\n%s", name, o.name, text)
			}
			for _, r := range refs {
				ref := r[1]
				if r[2] != "" {
					ref += ":" + r[2]
				}
				a, errs, code := runCLI(t, "", "expand", "-store", f.cliDir, ref)
				if code != 0 {
					t.Fatalf("cli expand %s: %s", ref, errs)
				}
				hc, b := f.httpDo("GET", "/expand/"+url.PathEscape(ref), nil, "")
				if hc != 200 {
					t.Fatalf("http expand %s: %d %s", ref, hc, b)
				}
				mc := f.mcpCall("expand", map[string]any{"ref": ref, "max_bytes": 64 << 20})
				if mc["isError"] == true {
					t.Fatalf("mcp expand %s: %s", ref, mcpText(mc))
				}
				if a != string(b) || a != mcpText(mc) {
					t.Fatalf("%s: expand %s differs (%d, %d, %d bytes)", name, ref, len(a), len(b), len(mcpText(mc)))
				}
				expansions++
			}
		}
	}

	// Run histories: the same sequence of runs through each front end gives
	// the same changes, run by run, and the same histories.
	histories := 0
	for _, path := range fixtures {
		name := filepath.Base(path)
		data, _ := os.ReadFile(path)
		if (testing.Short() || raceEnabled) && len(data) > 1<<20 {
			continue
		}
		other := filepath.Join(t.TempDir(), name)
		os.WriteFile(other, everyThirdLineDropped(data), 0o600)
		key := "parity " + name
		for i, p := range []string{path, other, other, path} {
			cli := f.cliReduce(p, "-key", key, "-budget", "2048")
			q := url.Values{"key": {key}, "budget": {"2048"}}
			web := f.httpReduce(p, q)
			m := f.mcpCall("reduce", map[string]any{"path": p, "key": key, "budget": 2048})
			viaMCP := m["structuredContent"].(map[string]any)
			if !reflect.DeepEqual(cli, web) || !reflect.DeepEqual(cli, viaMCP) {
				t.Fatalf("%s, run %d: the front ends differ\ncli:  %v\nhttp: %v\nmcp:  %v", name, i+1, cli["delta"], web["delta"], viaMCP["delta"])
			}
		}
		cliOut, _, _ := runCLI(t, "", "history", "-store", f.cliDir, "-json", key)
		_, webOut := f.httpDo("GET", "/runs/"+url.PathEscape(key), nil, "")
		m := f.mcpCall("history", map[string]any{"key": key})
		a, b, c := normal(decode(t, []byte(cliOut))), normal(decode(t, webOut)), normal(m["structuredContent"])
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, c) {
			t.Fatalf("%s: histories differ\ncli:  %v\nhttp: %v\nmcp:  %v", name, a, b, c)
		}
		histories++
	}
	var cliList, mcpList []any
	out, _, _ := runCLI(t, "", "history", "-store", f.cliDir, "-json")
	json.Unmarshal([]byte(out), &cliList)
	_, webData := f.httpDo("GET", "/runs", nil, "")
	var webList []any
	json.Unmarshal(webData, &webList)
	mcpList = f.mcpCall("history", map[string]any{})["structuredContent"].(map[string]any)["histories"].([]any)
	// Listed most recent first; the front ends recorded in the same order.
	if !reflect.DeepEqual(normal(cliList), normal(webList)) || !reflect.DeepEqual(normal(cliList), normal(mcpList)) {
		t.Fatalf("history lists differ:\n%v\n%v\n%v", cliList, webList, mcpList)
	}
	t.Logf("%d fixtures: %d reductions, %d expansions and %d histories the same through the command line, HTTP and MCP", len(fixtures), reductions, expansions, histories)
}

func mergeArgs(a, b map[string]any) map[string]any {
	for k, v := range b {
		a[k] = v
	}
	return a
}

func everyThirdLineDropped(data []byte) []byte {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		// A document: change one value instead of breaking it.
		if bytes.Contains(data, []byte(`"error"`)) {
			return bytes.Replace(data, []byte(`"error"`), []byte(`"Error"`), 1)
		}
		return bytes.Replace(data, []byte("<failure"), []byte("<skipped"), 1)
	}
	var out []byte
	for i, l := range bytes.SplitAfter(data, []byte("\n")) {
		if i%3 != 2 {
			out = append(out, l...)
		}
	}
	return out
}

// The report alone, as text, is the same through all three too.
func TestFrontEndsAgreeOnText(t *testing.T) {
	f := newFrontEnds(t)
	path := "../../test/fixtures/gotest-json.txt"
	cliText, _, _ := runCLI(t, "", "reduce", "-store", f.cliDir, path)
	file, _ := os.Open(path)
	defer file.Close()
	_, webText := f.httpDo("POST", "/reduce", file, "text/plain")
	mcp := mcpText(f.mcpCall("reduce", map[string]any{"path": path}))
	if cliText != string(webText) || cliText != mcp {
		t.Fatalf("texts differ:\n%s\n---\n%s\n---\n%s", cliText, webText, mcp)
	}
	data, _ := os.ReadFile(path)
	if mcp2 := mcpText(f.mcpCall("reduce", map[string]any{"text": string(data)})); mcp2 != cliText {
		t.Fatal("reducing text and reducing the file differ")
	}
	if !strings.Contains(cliText, "FAILED (7)") {
		t.Fatalf("unexpected report:\n%s", cliText)
	}
}
