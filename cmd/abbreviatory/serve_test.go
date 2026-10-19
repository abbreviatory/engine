package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHTTPService(t *testing.T) {
	srv := httptest.NewServer(newHandler(core{t.TempDir()}, "tok", 1<<20))
	defer srv.Close()
	do := func(method, path, body, token, accept string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data), resp.Header
	}
	log := "2026-10-18T08:00:00Z INFO up\n2026-10-18T08:00:01Z ERROR disk full\n"

	if code, body, _ := do("GET", "/healthz", "", "", ""); code != 200 || !strings.Contains(body, `"status": "ok"`) {
		t.Fatalf("healthz without a token: %d %s", code, body)
	}
	for _, tok := range []string{"", "wrong"} {
		if code, _, h := do("POST", "/reduce", log, tok, ""); code != 401 || h.Get("WWW-Authenticate") == "" {
			t.Fatalf("token %q: %d", tok, code)
		}
	}
	code, body, _ := do("POST", "/reduce?budget=600", log, "tok", "")
	var v map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil || v["format"] != "logs" {
		t.Fatalf("reduce: %d %s", code, body)
	}
	ref := v["ref"].(string)
	if code, body, _ := do("POST", "/reduce", log, "tok", "text/plain"); code != 200 || !strings.HasPrefix(body, "[abbreviatory] log") {
		t.Fatalf("text: %d %s", code, body)
	}
	if code, body, _ := do("GET", "/expand/"+ref+":L2", "", "tok", ""); code != 200 || body != "2026-10-18T08:00:01Z ERROR disk full\n" {
		t.Fatalf("expand: %d %q", code, body)
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/expand/nonsense", "", 400},
		{"GET", "/expand/" + ref + ":L99", "", 400},
		{"GET", "/expand/0123456789ab:g0", "", 404},
		{"POST", "/reduce?budget=5", log, 400},
		{"POST", "/reduce?budget=five", log, 400},
		{"POST", "/reduce?format=yaml", log, 400},
		{"POST", "/reduce?keep=(", log, 400},
		{"POST", "/reduce?full=maybe", log, 400},
		{"POST", "/reduce?format=json", `{"a": [1,`, 422},
		{"POST", "/reduce", string([]byte{0, 1, 2, 3, 0, 0, 0}), 422},
		{"POST", "/reduce", strings.Repeat("x\n", 600000), 413},
		{"GET", "/runs/nothing-here", "", 404},
		{"GET", "/reduce", "", 405},
		{"DELETE", "/runs", "", 405},
	} {
		if code, body, _ := do(c.method, c.path, c.body, "tok", ""); code != c.want || !strings.Contains(body, "error") && code != 405 {
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.path, code, c.want, body)
		}
	}
	// Histories, with a key that has slashes and spaces.
	key := "/home/me/app: go test ./..."
	q := "/reduce?key=" + strings.ReplaceAll(strings.ReplaceAll(key, " ", "%20"), "/", "%2F")
	do("POST", q, log, "tok", "")
	code, body, _ = do("POST", q, log, "tok", "")
	if code != 200 || !strings.Contains(body, `"run": 2`) {
		t.Fatalf("second run: %d %s", code, body)
	}
	if code, body, _ := do("GET", "/runs/%2Fhome%2Fme%2Fapp:%20go%20test%20.%2F...", "", "tok", "text/plain"); code != 200 || !strings.HasPrefix(body, key+"\nrun 2") {
		t.Fatalf("runs by key: %d %s", code, body)
	}
	if code, body, _ := do("GET", "/runs/me/app", "", "tok", ""); code != 200 || !strings.Contains(body, `"key": "/home/me/app: go test ./..."`) {
		t.Fatalf("runs by part of the key: %d %s", code, body)
	}
}

func TestHTTPNoToken(t *testing.T) {
	srv := httptest.NewServer(newHandler(core{t.TempDir()}, "", 1<<20))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/reduce", "text/plain", strings.NewReader("x ERROR one\n"))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("without a token configured: %v %v", err, resp.StatusCode)
	}
}

func TestServeAddress(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:1": true, "localhost:1": true, "[::1]:1": true, "0.0.0.0:1": false, ":7878": false, "10.0.0.5:1": false, "nonsense": false} {
		if isLoopback(addr) != want {
			t.Errorf("isLoopback(%q) = %v", addr, !want)
		}
	}
	_, errs, code := runCLI(t, "", "serve", "-addr", "0.0.0.0:0", "-store", t.TempDir())
	if code != 2 || !strings.Contains(errs, "set -token") {
		t.Fatalf("serving everywhere without a token: exit %d %s", code, errs)
	}
	_, errs, code = runCLI(t, "", "serve", "-max-body", "lots")
	if code != 2 || !strings.Contains(errs, "-max-body") {
		t.Fatalf("bad -max-body: exit %d %s", code, errs)
	}
}

// serve stops cleanly on a signal; started as a real process.
func TestServeProcess(t *testing.T) {
	if runtime.GOOS == "windows" || testing.Short() {
		t.Skip("needs a built binary and signals")
	}
	bin := buildBinary(t)
	cmd := execCommand(bin, "serve", "-addr", "127.0.0.1:0", "-store", t.TempDir())
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 200)
	n, _ := stderr.Read(buf)
	line := string(buf[:n])
	i := strings.Index(line, "http://")
	if i < 0 {
		cmd.Process.Kill()
		t.Fatalf("no address in %q", line)
	}
	url := strings.TrimSpace(line[i:])
	resp, err := http.Get(url + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		cmd.Process.Kill()
		t.Fatalf("healthz: %v", err)
	}
	cmd.Process.Signal(stopSignals[1])
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != 0 {
			t.Fatalf("exit %d after a signal", code)
		}
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		t.Fatal("serve didn't stop on a signal")
	}
}

// Review of 0.3: without a token the service answers this machine only,
// and not web pages: no other host names (DNS rebinding), no foreign
// Origin (cross-site requests).
func TestHTTPLocalOnly(t *testing.T) {
	h := newHandler(core{t.TempDir()}, "", 1<<20)
	do := func(method, target, host, origin string) (int, string) {
		req := httptest.NewRequest(method, target, strings.NewReader("x ERROR secret password=hunter2\n"))
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, c := range []struct {
		method, target, host, origin string
		want                         int
	}{
		{"POST", "/reduce?key=proj", "localhost:7878", "", 200},
		{"POST", "/reduce", "127.0.0.1:7878", "", 200},
		{"POST", "/reduce", "[::1]:7878", "", 200},
		{"POST", "/reduce", "localhost:7878", "http://localhost:3000", 200},
		{"GET", "/runs", "attacker.example:7878", "", 403},
		{"GET", "/runs", "attacker.example", "", 403},
		{"POST", "/reduce?key=proj", "localhost:7878", "http://evil.example", 403},
		{"POST", "/reduce?key=proj", "localhost:7878", "null", 403},
		{"GET", "/healthz", "evil.example", "", 403},
	} {
		if code, body := do(c.method, c.target, c.host, c.origin); code != c.want {
			t.Errorf("%s %s Host %s Origin %q: %d, want %d: %s", c.method, c.target, c.host, c.origin, code, c.want, body)
		}
	}
}

func TestHTTPDetails(t *testing.T) {
	srv := httptest.NewServer(newHandler(core{t.TempDir()}, "tok", 1<<30))
	defer srv.Close()
	get := func(method, path, auth string, body io.Reader) (int, string, string) {
		req, _ := http.NewRequest(method, srv.URL+path, body)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(data)
	}
	for _, auth := range []string{"Bearer tok", "bearer tok", "BEARER  tok"} {
		if code, _, _ := get("GET", "/runs", auth, nil); code != 200 {
			t.Errorf("%q: %d", auth, code)
		}
	}
	for _, auth := range []string{"Bearer", "Basic tok", "Bearer tokx", "tok"} {
		if code, _, _ := get("GET", "/runs", auth, nil); code != 401 {
			t.Errorf("%q: %d, want 401", auth, code)
		}
	}
	for _, c := range []struct {
		method, path string
		want         int
	}{{"GET", "/nothing", 404}, {"GET", "/reduce", 405}, {"DELETE", "/runs", 405}} {
		code, ct, body := get(c.method, c.path, "Bearer tok", nil)
		if code != c.want || !strings.HasPrefix(ct, "application/json") || !strings.Contains(body, `"error"`) {
			t.Errorf("%s %s: %d %s %s", c.method, c.path, code, ct, body)
		}
	}
	// /runs/ with nothing after it is the list.
	if code, _, body := get("GET", "/runs/", "Bearer tok", nil); code != 200 || !strings.HasPrefix(strings.TrimSpace(body), "[") {
		t.Errorf("/runs/: %d %s", code, body)
	}
	// A large original expands, streamed.
	big := strings.Repeat("2026-10-18T08:00:00Z INFO tick 12345\n", 200000) + "x ERROR end\n"
	code, _, body := get("POST", "/reduce", "Bearer tok", strings.NewReader(big))
	var v map[string]any
	if code != 200 || json.Unmarshal([]byte(body), &v) != nil {
		t.Fatalf("reduce: %d", code)
	}
	code, ct, whole := get("GET", "/expand/"+v["ref"].(string), "Bearer tok", nil)
	if code != 200 || ct != "application/octet-stream" || whole != big {
		t.Fatalf("whole expand: %d %s, %d bytes of %d", code, ct, len(whole), len(big))
	}
	code, _, part := get("GET", "/expand/"+v["ref"].(string)+":L200001", "Bearer tok", nil)
	if code != 200 || part != "x ERROR end\n" {
		t.Fatalf("last line: %d %q", code, part)
	}
}

// Review of 0.3, second pass: slow uploads hold up nobody, and a JSON
// pointer reference expands with its slashes as they are.
func TestHTTPSlowUploadsAndPointers(t *testing.T) {
	srv := httptest.NewServer(newHandler(core{t.TempDir()}, "", 1<<20))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	for i := 0; i < runtime.NumCPU()+2; i++ {
		conn, err := net.Dial("tcp", host)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		io.WriteString(conn, "POST /reduce HTTP/1.1\r\nHost: "+host+"\r\nContent-Length: 1000000\r\n\r\n0123456789")
	}
	time.Sleep(200 * time.Millisecond)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(srv.URL+"/reduce?format=json", "application/octet-stream", strings.NewReader(`{"items": [{"id": 1, "error": "boom"}]}`))
	if err != nil {
		t.Fatalf("held up by slow uploads: %v", err)
	}
	var v map[string]any
	json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	ref, _ := v["ref"].(string)
	for _, path := range []string{"/expand/" + ref + ":/items/0/error", "/expand/" + ref + ":%2Fitems%2F0%2Ferror"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != "\"boom\"\n" {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, body)
		}
	}
}
