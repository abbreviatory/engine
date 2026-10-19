package abbreviatory

import (
	"strings"
	"testing"
)

func TestHTTPCompactJSONBody(t *testing.T) {
	body := `{"ok":false,"items":[` + strings.Repeat(`{"id":1,"name":"x"},`, 200) + `{"id":2,"error":"quota exceeded"}],"error":{"code":"E_LIMIT"}}`
	in := "HTTP/2 429\r\ncontent-type: application/json\r\nretry-after: 30\r\nx-cache: miss\r\ndate: Mon, 19 Oct 2026 03:50:31 GMT\r\n\r\n" + body + "\n"
	res := reduceAs(t, in, "http", 1500)
	// Both error fields are on the one line, so they're one record.
	wantIn(t, res.Text, "http: 1 response: 429; body ", "application/json",
		"ERRORS (2 kinds, 2 lines):", "- HTTP/2 429", "body /items/200/error: \"quota exceeded\" (and 1 more error field)",
		"retry-after: 30", "x2 (other headers)")
}

func TestHTTPRedirectChainAndCurlError(t *testing.T) {
	in := "HTTP/1.1 302 Found\nLocation: https://example.com/b\nContent-Length: 0\n\n" +
		"HTTP/1.1 503 Service Unavailable\nContent-Type: text/plain\nRetry-After: 120\n\nupstream connect error\nERROR: backend pool empty\n" + strings.Repeat("retrying upstream 10.0.0.7:8080\n", 30) +
		"curl: (22) The requested URL returned error: 503\n"
	res := reduceAs(t, in, "http", 2000)
	wantIn(t, res.Text, "http: 2 responses: 302 Found -> 503 Service Unavailable", "Location: https://example.com/b")
	if res.Records["error"] < 3 {
		t.Errorf("%d errors, want the 503, the body's ERROR line and curl's error:\n%s", res.Records["error"], res.Text)
	}
}

func TestHTTPVerboseConnectFailure(t *testing.T) {
	in := "*   Trying 10.0.0.1:443...\n* connect to 10.0.0.1 port 443 failed: Connection refused\n* Failed to connect to api.example port 443 after 3 ms: Couldn't connect to server\n* Closing connection\ncurl: (7) Failed to connect to api.example port 443 after 3 ms: Couldn't connect to server\n"
	res, err := Reduce(strings.NewReader(in), Options{Format: "http", Budget: 800})
	if err != nil {
		t.Fatal(err)
	}
	checkCoverage(t, "connect failure", []byte(in), res)
	if res.Records["error"] != 3 {
		t.Errorf("%d errors, want 3:\n%s", res.Records["error"], res.Text)
	}
}

// A body that says it's JSON and isn't is read like a log.
func TestHTTPBrokenJSONBody(t *testing.T) {
	in := "HTTP/1.1 500 Internal Server Error\nContent-Type: application/json\n\n{\"error\": \"truncated\n<html>proxy error</html>\n"
	res := reduceAs(t, in, "http", 700)
	wantIn(t, res.Text, "HTTP/1.1 500 Internal Server Error")
}
