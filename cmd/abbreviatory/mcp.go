package main

// abbreviatory mcp: a Model Context Protocol server over standard input and
// output, for coding agents. It speaks JSON-RPC 2.0, one message per line,
// and offers four tools: reduce, run, expand and history.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
)

// mcpVersions are the protocol versions the server speaks, newest first.
var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Limits for the MCP server.
const (
	mcpMaxMessage    = 256 << 20 // the largest message read
	mcpDefaultExpand = 4 << 20   // what expand returns at most unless asked
	mcpMaxExpand     = 64 << 20
	mcpDefaultTime   = 10 * time.Minute
)

func cmdMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := storeFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory mcp [flags]\n\nServes the tools reduce, run, expand and history to a coding agent over\nthe Model Context Protocol on standard input and output. Add it to the\nagent's MCP settings as the command \"abbreviatory mcp\".\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	s := &mcpServer{c: core{*dir}, out: stdout, running: map[string]chan struct{}{}}
	if err := s.serve(stdin); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	return 0
}

type mcpServer struct {
	c       core
	out     io.Writer
	mu      sync.Mutex // guards out and running
	running map[string]chan struct{}
	wg      sync.WaitGroup
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// serve reads messages until the input ends. Each request is answered in its
// own goroutine, so a long run doesn't hold up a ping or a cancellation.
func (s *mcpServer) serve(in io.Reader) error {
	r := bufio.NewReaderSize(in, 1<<20)
	defer s.wg.Wait()
	for {
		line, err := readMessage(r)
		if len(bytes.TrimSpace(line)) > 0 {
			s.handle(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		part, err := r.ReadSlice('\n')
		buf = append(buf, part...)
		if len(buf) > mcpMaxMessage {
			// Skip the rest of it.
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			return []byte(`{"jsonrpc":"2.0","method":"(too large)"}`), err
		}
		if err != bufio.ErrBufferFull {
			return buf, err
		}
	}
}

func (s *mcpServer) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "error": rpcError{-32603, err.Error()}})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out.Write(append(data, '\n'))
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *mcpServer) fail(id json.RawMessage, code int, msg string) {
	if id == nil {
		id = json.RawMessage("null")
	}
	s.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": rpcError{code, msg}})
}

func (s *mcpServer) handle(line []byte) {
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		s.fail(nil, -32700, "parse error: "+err.Error())
		return
	}
	if m.Method == "(too large)" {
		s.fail(nil, -32600, fmt.Sprintf("message larger than %d MB", mcpMaxMessage>>20))
		return
	}
	notification := len(m.ID) == 0
	switch m.Method {
	case "notifications/initialized", "notifications/roots/list_changed":
		return
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		json.Unmarshal(m.Params, &p)
		s.mu.Lock()
		if c, ok := s.running[string(p.RequestID)]; ok {
			close(c)
			delete(s.running, string(p.RequestID))
		}
		s.mu.Unlock()
		return
	}
	if notification {
		return
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		version := mcpVersions[0]
		for _, v := range mcpVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		s.reply(m.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "abbreviatory", "version": abbreviatory.Version},
			"instructions":    mcpInstructions,
		})
	case "ping":
		s.reply(m.ID, map[string]any{})
	case "tools/list":
		s.reply(m.ID, map[string]any{"tools": mcpTools()})
	case "tools/call":
		cancel := make(chan struct{})
		s.mu.Lock()
		s.running[string(m.ID)] = cancel
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			result, rerr := s.call(m.Params, cancel)
			s.mu.Lock()
			_, live := s.running[string(m.ID)]
			delete(s.running, string(m.ID))
			s.mu.Unlock()
			if !live {
				return // cancelled: no answer is expected
			}
			if rerr != nil {
				s.fail(m.ID, rerr.Code, rerr.Message)
				return
			}
			s.reply(m.ID, result)
		}()
	default:
		s.fail(m.ID, -32601, "unknown method "+m.Method)
	}
}

const mcpInstructions = "Abbreviatory reduces large tool output (test runs, build and application logs, JSON) to a short report within a byte budget: failures, errors and warnings first, repeats folded into one line with a count, every left-out part reachable through a reference such as [ref 3f2a9c1b7e04:g12]. Use run to run a test or build command instead of reading its whole output; with a history it says what changed since the last run (new, fixed, still failing, flaky), and one line when nothing did. Use expand on a reference to see the original lines."

// toolArgs are the arguments every tool may get; each uses its own.
type toolArgs struct {
	Text      *string  `json:"text"`
	Path      string   `json:"path"`
	Command   []string `json:"command"`
	Cwd       string   `json:"cwd"`
	Timeout   int      `json:"timeout_seconds"`
	Ref       string   `json:"ref"`
	MaxBytes  int      `json:"max_bytes"`
	Format    string   `json:"format"`
	Budget    int      `json:"budget"`
	Preserve  *string  `json:"preserve"`
	Keep      []string `json:"keep"`
	Key       string   `json:"key"`
	Full      bool     `json:"full"`
	NoStore   bool     `json:"no_store"`
	NoHistory bool     `json:"no_history"`
	Stdin     *string  `json:"stdin"`
}

func (a toolArgs) request() request {
	return request{Format: a.Format, Budget: a.Budget, Preserve: a.Preserve, Keep: a.Keep, Key: a.Key, Full: a.Full, NoStore: a.NoStore}
}

// call runs a tool. Problems with the tool's work come back as a result
// with isError set, as the protocol asks; only an unknown tool or arguments
// that aren't JSON are protocol errors.
func (s *mcpServer) call(params json.RawMessage, cancel <-chan struct{}) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{-32602, "invalid params: " + err.Error()}
	}
	var a toolArgs
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		dec := json.NewDecoder(bytes.NewReader(p.Arguments))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			return toolError("bad arguments for " + p.Name + ": " + err.Error()), nil
		}
	}
	switch p.Name {
	case "reduce":
		return s.toolReduce(a, cancel), nil
	case "run":
		return s.toolRun(a, cancel), nil
	case "expand":
		return s.toolExpand(a), nil
	case "history":
		return s.toolHistory(a), nil
	}
	return nil, &rpcError{-32602, "unknown tool " + p.Name}
}

func toolError(msg string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true}
}

func toolText(text string, structured any) map[string]any {
	out := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
	if structured != nil {
		out["structuredContent"] = structured
	}
	return out
}

func (s *mcpServer) toolReduce(a toolArgs, cancel <-chan struct{}) map[string]any {
	var in io.Reader
	switch {
	case a.Text != nil && a.Path != "":
		return toolError("give text or path, not both")
	case a.Text != nil:
		in = strings.NewReader(*a.Text)
	case a.Path != "":
		f, err := os.Open(a.Path)
		if err != nil {
			return toolError(err.Error())
		}
		defer f.Close()
		in = f
	default:
		return toolError("give the output to reduce as text, or a file as path")
	}
	o, err := s.c.reduce(in, a.request(), cancel)
	if err != nil {
		return toolError(err.Error())
	}
	return withWarnings(toolText(o.Res.Text, o.JSON()), o.Warnings)
}

func (s *mcpServer) toolRun(a toolArgs, cancel <-chan struct{}) map[string]any {
	if len(a.Command) == 0 {
		return toolError("give the command as an array, such as [\"go\", \"test\", \"./...\"]")
	}
	timeout := mcpDefaultTime
	if a.Timeout > 0 {
		timeout = time.Duration(a.Timeout) * time.Second
	}
	x := execOpts{Dir: a.Cwd, Timeout: timeout, Cancel: cancel, Group: true}
	if a.Stdin != nil {
		x.Stdin = strings.NewReader(*a.Stdin)
	}
	e, err := execute(a.Command, x)
	if err != nil {
		var se *startError
		if errors.As(err, &se) {
			return toolError(fmt.Sprintf("%v (exit status %d)", err, se.Status))
		}
		return toolError(err.Error())
	}
	defer e.Close()
	q := a.request()
	q.Command = strings.Join(a.Command, " ")
	status := e.Status
	q.Exit = &status
	if a.NoHistory {
		q.Key = ""
	} else if q.Key == "" {
		q.Key = defaultKey(a.Cwd, q.Command)
	}
	o, err := s.c.reduce(e.Output, q, cancel)
	if err != nil {
		return toolError(fmt.Sprintf("the command exited with status %d, and its output could not be reduced: %v", status, err))
	}
	o.Warnings = append(e.Warnings, o.Warnings...)
	result := toolText(o.Res.Text, o.JSON())
	if a.NoHistory {
		// Without a history the report doesn't show the exit status.
		result["content"] = append(result["content"].([]map[string]any), map[string]any{"type": "text", "text": fmt.Sprintf("exit status %d", status)})
	}
	return withWarnings(result, o.Warnings)
}

func withWarnings(result map[string]any, warnings []string) map[string]any {
	if len(warnings) > 0 {
		result["content"] = append(result["content"].([]map[string]any), map[string]any{"type": "text", "text": "warning: " + strings.Join(warnings, "\nwarning: ")})
	}
	return result
}

func (s *mcpServer) toolExpand(a toolArgs) map[string]any {
	if a.Ref == "" {
		return toolError("give a reference such as 3f2a9c1b7e04:g12")
	}
	limit := mcpDefaultExpand
	if a.MaxBytes > 0 {
		limit = min(a.MaxBytes, mcpMaxExpand)
	}
	data, err := s.c.expand(a.Ref)
	if err != nil {
		return toolError(err.Error())
	}
	if len(data) > limit {
		return toolError(fmt.Sprintf("%s is %d bytes, more than the %d this returns; expand a part of it, such as a group (:g12), a kind (:error) or a line range (:L1-500), or raise max_bytes (up to %d)", a.Ref, len(data), limit, mcpMaxExpand))
	}
	return toolText(string(data), nil)
}

func (s *mcpServer) toolHistory(a toolArgs) map[string]any {
	if a.Key == "" {
		all, err := s.c.histories()
		if err != nil {
			return toolError(err.Error())
		}
		return toolText(historiesText(all), map[string]any{"histories": historiesJSON(all)})
	}
	h, err := s.c.history(a.Key)
	if err != nil {
		return toolError(err.Error())
	}
	return toolText(historyText(h), historyJSON(h))
}

// mcpTools describes the tools for tools/list.
func mcpTools() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	common := func(props map[string]any) map[string]any {
		props["format"] = str("auto (the default), test-output, logs, json, or one of: " + strings.Join(abbreviatory.FormatNames(), ", "))
		props["budget"] = integer(fmt.Sprintf("most bytes of the report; default %d, at least %d", abbreviatory.DefaultBudget, abbreviatory.MinBudget))
		props["preserve"] = str("what is always shown or referenced: any of errors, failures, warnings, comma separated; default all three, empty for none")
		props["keep"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "regular expressions; matching lines are kept like errors"}
		props["full"] = boolean("with a history, the whole report even when nothing changed since the last run")
		props["no_store"] = boolean("don't keep the original; the report then has no references")
		return props
	}
	return []map[string]any{
		{
			"name":        "reduce",
			"title":       "Reduce output",
			"description": "Reduce tool output (a test run, a build or application log, a JSON document) to a short report: failures, errors and warnings first, repeats folded with a count, within the byte budget. Parts left out carry references for expand. Give key to compare with the last reduction under that key.",
			"inputSchema": map[string]any{"type": "object", "properties": common(map[string]any{
				"text": str("the output to reduce"),
				"path": str("a file to reduce instead of text"),
				"key":  str("a history name: compare with the last run under it and record this one"),
			})},
		},
		{
			"name":        "run",
			"title":       "Run a command and reduce its output",
			"description": "Run a command (tests, a build, a linter), reduce what it prints (standard output and standard error together) and compare with its last run: new failures, fixed ones, still failing, flaky. One line when nothing changed. The report shows the exit status.",
			"inputSchema": map[string]any{"type": "object", "required": []string{"command"}, "properties": common(map[string]any{
				"command":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "the command and its arguments, such as [\"go\", \"test\", \"./...\"]; not run through a shell"},
				"cwd":             str("the directory to run it in; default the server's"),
				"timeout_seconds": integer("stop the command after this long; default 600"),
				"stdin":           str("text to give the command on standard input"),
				"key":             str("the history name; default the project directory and the command"),
				"no_history":      boolean("don't compare with the last run or record this one"),
			})},
		},
		{
			"name":        "expand",
			"title":       "Expand a reference",
			"description": "Return the original lines a reference from a report points to, byte for byte. References look like 3f2a9c1b7e04:g12 (a group), :error or :fail (every line of a kind), :L10-40 (a line range), or no selector (the whole input).",
			"inputSchema": map[string]any{"type": "object", "required": []string{"ref"}, "properties": map[string]any{
				"ref":       str("the reference, with or without the brackets and the word ref"),
				"max_bytes": integer(fmt.Sprintf("the most bytes to return; default %d, up to %d", mcpDefaultExpand, mcpMaxExpand)),
			}},
		},
		{
			"name":        "history",
			"title":       "List run histories",
			"description": "List the run histories, or with key the runs of one: when, how many failing, how many new and fixed, the exit status and the reference of each.",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
				"key": str("the history's key, or a part of it that matches one"),
			}},
		},
	}
}
