package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mcpSession drives the MCP server over pipes, as an agent would.
type mcpSession struct {
	t    *testing.T
	in   io.WriteCloser
	out  *bufio.Reader
	done chan int
}

func startMCP(t *testing.T, dir string) *mcpSession {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &mcpSession{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan int, 1)}
	go func() {
		code := run([]string{"mcp", "-store", dir}, inR, outW, io.Discard)
		outW.Close()
		s.done <- code
	}()
	t.Cleanup(func() { inW.Close() })
	return s
}

func (s *mcpSession) send(msg string) {
	if _, err := io.WriteString(s.in, msg+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

func (s *mcpSession) recv() map[string]any {
	type line struct {
		b   []byte
		err error
	}
	ch := make(chan line, 1)
	go func() {
		b, err := s.out.ReadBytes('\n')
		ch <- line{b, err}
	}()
	select {
	case l := <-ch:
		if l.err != nil {
			s.t.Fatalf("reading from the server: %v", l.err)
		}
		var m map[string]any
		if err := json.Unmarshal(l.b, &m); err != nil {
			s.t.Fatalf("not JSON: %s", l.b)
		}
		return m
	case <-time.After(20 * time.Second):
		s.t.Fatal("no answer from the server")
		return nil
	}
}

func TestMCPProtocol(t *testing.T) {
	s := startMCP(t, t.TempDir())
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`)
	m := s.recv()
	r := m["result"].(map[string]any)
	if r["protocolVersion"] != "2025-03-26" || r["serverInfo"].(map[string]any)["name"] != "abbreviatory" {
		t.Fatalf("initialize: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.send(`{"jsonrpc":"2.0","id":"a","method":"ping"}`)
	if m := s.recv(); m["id"] != "a" || m["result"] == nil {
		t.Fatalf("ping: %v", m)
	}
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := s.recv()["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		tm := tl.(map[string]any)
		names = append(names, tm["name"].(string))
		if tm["inputSchema"].(map[string]any)["type"] != "object" || tm["description"] == "" {
			t.Fatalf("tool %v", tm)
		}
	}
	if strings.Join(names, ",") != "reduce,run,expand,history" {
		t.Fatalf("tools: %v", names)
	}
	// An unknown version gets the newest.
	s.send(`{"jsonrpc":"2.0","id":3,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := s.recv()["result"].(map[string]any)["protocolVersion"]; v != mcpVersions[0] {
		t.Fatalf("version for an unknown one: %v", v)
	}
	for _, c := range []struct {
		msg  string
		code float64
	}{
		{`{"jsonrpc":"2.0","id":4,"method":"resources/list"}`, -32601},
		{`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"frobnicate","arguments":{}}}`, -32602},
		{`not json`, -32700},
	} {
		s.send(c.msg)
		m := s.recv()
		if e, ok := m["error"].(map[string]any); !ok || e["code"] != c.code {
			t.Fatalf("%s: %v", c.msg, m)
		}
	}
	// Tool problems come back as results marked as errors.
	for _, args := range []string{`{}`, `{"text":"x","path":"y"}`, `{"text":"x","budget":5}`, `{"text":"x","bogus":1}`, `{"path":"/no/such/file"}`} {
		s.send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"reduce","arguments":` + args + `}}`)
		r := s.recv()["result"].(map[string]any)
		if r["isError"] != true {
			t.Fatalf("reduce %s: %v", args, r)
		}
	}
	s.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"reduce","arguments":{"text":"2026-10-18T08:00:01Z ERROR disk full\n"}}}`)
	r = s.recv()["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	if r["isError"] != false || !strings.Contains(text, "ERROR disk full") {
		t.Fatalf("reduce: %v", r)
	}
	ref := r["structuredContent"].(map[string]any)["ref"].(string)
	s.send(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"expand","arguments":{"ref":"` + ref + `","max_bytes":10}}}`)
	if r := s.recv()["result"].(map[string]any); r["isError"] != true || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "raise max_bytes") {
		t.Fatalf("expand over the limit: %v", r)
	}
	s.in.Close()
	if code := <-s.done; code != 0 {
		t.Fatalf("exit %d at the end of input", code)
	}
}

func TestMCPRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	s := startMCP(t, dir)
	call := func(id, args string) map[string]any {
		s.send(`{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"run","arguments":` + args + `}}`)
		return s.recv()["result"].(map[string]any)
	}
	texts := func(r map[string]any) []string {
		var out []string
		for _, c := range r["content"].([]any) {
			out = append(out, c.(map[string]any)["text"].(string))
		}
		return out
	}
	r := call("1", `{"command":["sh","-c","echo x ERROR boom; exit 3"],"key":"job"}`)
	if r["isError"] != false || !strings.Contains(texts(r)[0], "run 1, exit status 3") || r["structuredContent"].(map[string]any)["exit"] != float64(3) {
		t.Fatalf("run: %v", r)
	}
	r = call("2", `{"command":["sh","-c","echo x ERROR boom; exit 3"],"key":"job"}`)
	if !strings.Contains(texts(r)[0], "run 2, exit status 3, identical to run 1") {
		t.Fatalf("second run: %v", texts(r))
	}
	r = call("3", `{"command":["sh","-c","echo fine"],"no_history":true}`)
	if tx := texts(r); len(tx) != 2 || tx[1] != "exit status 0" {
		t.Fatalf("no history: %v", tx)
	}
	r = call("4", `{"command":["no-such-command-abbreviatory"]}`)
	if r["isError"] != true || !strings.Contains(texts(r)[0], "exit status 127") {
		t.Fatalf("missing command: %v", r)
	}
	r = call("5", `{"command":["sh","-c","echo in $(pwd); cat"],"cwd":"`+dir+`","stdin":"from stdin\n","no_history":true}`)
	if !strings.Contains(texts(r)[0], "in "+dir) || !strings.Contains(texts(r)[0], "from stdin") {
		t.Fatalf("cwd and stdin: %v", texts(r))
	}
	start := time.Now()
	r = call("6", `{"command":["sh","-c","echo started; sleep 30"],"timeout_seconds":1,"no_history":true}`)
	if time.Since(start) > 10*time.Second || !strings.Contains(strings.Join(texts(r), "\n"), "time limit") {
		t.Fatalf("timeout after %s: %v", time.Since(start), texts(r))
	}
	// A cancelled run is stopped and gets no answer; the next request does.
	s.send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"run","arguments":{"command":["sleep","30"],"no_history":true}}}`)
	time.Sleep(300 * time.Millisecond)
	start = time.Now()
	s.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`)
	s.send(`{"jsonrpc":"2.0","id":8,"method":"ping"}`)
	if m := s.recv(); m["id"] != float64(8) {
		t.Fatalf("after a cancel: %v", m)
	}
	// The default key is the directory and the command.
	call("9", `{"command":["sh","-c","echo x ERROR a"],"cwd":"`+dir+`"}`)
	s.send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"history","arguments":{}}}`)
	if h := texts(s.recv()["result"].(map[string]any))[0]; !strings.Contains(h, dir+": sh -c echo x ERROR a") || !strings.Contains(h, "job") {
		t.Fatalf("history: %s", h)
	}
	s.in.Close()
	select {
	case <-s.done:
	case <-time.After(15 * time.Second):
		t.Fatal("the cancelled run wasn't stopped")
	}
	if time.Since(start) > 15*time.Second {
		t.Fatal("cancelling took too long")
	}
}

func buildBinary(t *testing.T) string {
	bin := filepath.Join(t.TempDir(), "abbreviatory")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Skipf("can't build: %v %s", err, out)
	}
	return bin
}

func execCommand(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }

// Review of 0.3: batches and non-objects are invalid requests; a response
// from the client gets no answer; the end of input stops running commands.
func TestMCPMessagesAndShutdown(t *testing.T) {
	s := startMCP(t, t.TempDir())
	for _, msg := range []string{`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, `42`, `"ping"`} {
		s.send(msg)
		if e, ok := s.recv()["error"].(map[string]any); !ok || e["code"] != float64(-32600) {
			t.Fatalf("%s: not an invalid request", msg)
		}
	}
	s.send(`{"jsonrpc":"2.0","id":99,"result":{}}`)
	s.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if m := s.recv(); m["id"] != float64(2) {
		t.Fatalf("a client response was answered: %v", m)
	}
	if runtime.GOOS == "windows" {
		return
	}
	marker := filepath.Join(t.TempDir(), "still-running")
	s.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run","arguments":{"command":["sh","-c","sleep 30 & echo $! > ` + marker + `; wait"],"no_history":true}}}`)
	time.Sleep(500 * time.Millisecond)
	start := time.Now()
	s.in.Close()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server waited for the running command")
	}
	if d := time.Since(start); d > 7*time.Second {
		t.Fatalf("stopping took %s", d)
	}
	pid, _ := os.ReadFile(marker)
	if p := strings.TrimSpace(string(pid)); p != "" {
		// The signal lands a moment later; a zombie left for init counts
		// as gone.
		alive := func() bool {
			st, err := os.ReadFile("/proc/" + p + "/stat")
			if err != nil {
				return exec.Command("kill", "-0", p).Run() == nil
			}
			f := strings.Fields(string(st))
			return len(f) > 2 && f[2] != "Z"
		}
		for i := 0; i < 30 && alive(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if alive() {
			exec.Command("kill", "-9", p).Run()
			t.Fatalf("what the command started (pid %s) is still running", p)
		}
	}
}
