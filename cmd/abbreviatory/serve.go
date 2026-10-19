package main

// abbreviatory serve: the HTTP service. It answers on this machine only
// unless a token is set, and then asks every caller for it.

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
	"github.com/abbreviatory/engine/store"
)

// DefaultAddr is where serve listens unless told otherwise.
const DefaultAddr = "127.0.0.1:7878"

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", DefaultAddr, "address to listen on")
	token := fs.String("token", "", "require this bearer token (default $ABBREVIATORY_TOKEN); needed to listen beyond this machine")
	dir := storeFlag(fs)
	maxBody := fs.String("max-body", "1GB", "largest input accepted")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory serve [flags]\n\nServes reduce, expand and run histories over HTTP:\n  POST /reduce        the request body is the input; returns JSON (or the\n                      report alone with Accept: text/plain)\n  GET  /expand/{ref}  what a reference points to\n  GET  /runs          the histories; GET /runs/{key} one history's runs\n  GET  /healthz       {\"status\": \"ok\"}\n\n")
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
	if *token == "" {
		*token = os.Getenv("ABBREVIATORY_TOKEN")
	}
	limit, err := parseSize(*maxBody)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory: -max-body:", err)
		return 2
	}
	if !isLoopback(*addr) && *token == "" {
		fmt.Fprintf(stderr, "abbreviatory: %s is reachable from other machines; set -token (or $ABBREVIATORY_TOKEN) to serve there\n", *addr)
		return 2
	}
	c := core{*dir}
	if _, err := c.store(); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	srv := &http.Server{
		Handler:           newHandler(c, *token, limit),
		ReadHeaderTimeout: 10 * time.Second,
		// A body may be a large log on a slow link, but not forever.
		ReadTimeout: 30 * time.Minute,
		IdleTimeout: 2 * time.Minute,
	}
	fmt.Fprintf(stderr, "abbreviatory %s: serving on http://%s\n", abbreviatory.Version, ln.Addr())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, stopSignals...)
	defer signal.Stop(stop)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	case <-stop:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		return 0
	}
}

// isLoopback reports whether addr only listens on this machine.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newHandler is the HTTP API over c. With a token, every request but the
// health check must carry it as "Authorization: Bearer TOKEN". Without one
// the service is for this machine only, and it also turns away what a web
// page in the user's browser could send it: a request for another host name
// (DNS rebinding) or with a foreign Origin (a cross-site form or fetch).
func newHandler(c core, token string, maxBody int64) http.Handler {
	// Reductions and expansions take memory and CPU; a few at a time.
	heavy := make(chan struct{}, max(2, runtime.NumCPU()))
	limited := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			select {
			case heavy <- struct{}{}:
				defer func() { <-heavy }()
				h(w, r)
			case <-r.Context().Done():
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": abbreviatory.Version})
	})
	mux.HandleFunc("POST /reduce", limited(func(w http.ResponseWriter, r *http.Request) {
		q, err := requestFromQuery(r)
		if err != nil {
			replyError(w, err)
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxBody)
		o, err := c.reduce(body, q, r.Context().Done())
		if err != nil {
			replyError(w, err)
			return
		}
		if wantsText(r) {
			for _, warn := range o.Warnings {
				w.Header().Add("Abbreviatory-Warning", warn)
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, o.Res.Text)
			return
		}
		replyJSON(w, http.StatusOK, o.JSON())
	}))
	mux.HandleFunc("GET /expand/{ref}", limited(func(w http.ResponseWriter, r *http.Request) {
		lw := &lazyWriter{w: w}
		if err := c.expand(lw, r.PathValue("ref")); err != nil && !lw.started {
			replyError(w, err)
			return
		}
		if !lw.started {
			lw.start() // an empty part
		}
	}))
	runs := func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("key")
		if key == "" {
			all, err := c.histories()
			if err != nil {
				replyError(w, err)
				return
			}
			if wantsText(r) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				io.WriteString(w, historiesText(all))
				return
			}
			replyJSON(w, http.StatusOK, historiesJSON(all))
			return
		}
		h, err := c.history(key)
		if err != nil {
			replyError(w, err)
			return
		}
		if wantsText(r) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, historyText(h))
			return
		}
		replyJSON(w, http.StatusOK, historyJSON(h))
	}
	mux.HandleFunc("GET /runs", runs)
	mux.HandleFunc("GET /runs/{key...}", runs)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			if !loopbackHost(r.Host) {
				replyJSON(w, http.StatusForbidden, map[string]any{"error": "this service answers for localhost only; set a token to serve other host names"})
				return
			}
			if o := r.Header.Get("Origin"); o != "" && !loopbackOrigin(o) {
				replyJSON(w, http.StatusForbidden, map[string]any{"error": "requests from web pages aren't accepted"})
				return
			}
		} else if r.URL.Path != "/healthz" && !validBearer(r.Header.Get("Authorization"), token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="abbreviatory"`)
			replyJSON(w, http.StatusUnauthorized, map[string]any{"error": "a bearer token is required"})
			return
		}
		mux.ServeHTTP(&jsonErrors{ResponseWriter: w}, r)
	})
}

// validBearer checks an Authorization header; the scheme is case-insensitive.
func validBearer(header, token string) bool {
	scheme, got, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(got)), []byte(token)) == 1
}

// loopbackHost reports whether a Host header names this machine.
func loopbackHost(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// loopbackOrigin reports whether an Origin header is a page on this machine.
func loopbackOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && loopbackHost(u.Host)
}

// lazyWriter sets the headers for raw bytes on the first write, so an error
// found before anything is written can still get its own status.
type lazyWriter struct {
	w       http.ResponseWriter
	started bool
}

func (l *lazyWriter) start() {
	l.started = true
	l.w.Header().Set("Content-Type", "application/octet-stream")
	l.w.WriteHeader(http.StatusOK)
}

func (l *lazyWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.start()
	}
	return l.w.Write(p)
}

// jsonErrors turns the mux's own plain-text 404 and 405 answers into JSON,
// as every other error is.
type jsonErrors struct {
	http.ResponseWriter
	swallow bool
}

func (j *jsonErrors) WriteHeader(code int) {
	if (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) && strings.HasPrefix(j.Header().Get("Content-Type"), "text/plain") {
		j.Header().Set("Content-Type", "application/json")
		j.Header().Del("X-Content-Type-Options")
		j.ResponseWriter.WriteHeader(code)
		writeJSON(j.ResponseWriter, map[string]any{"error": http.StatusText(code)})
		j.swallow = true
		return
	}
	j.ResponseWriter.WriteHeader(code)
}

func (j *jsonErrors) Write(p []byte) (int, error) {
	if j.swallow {
		return len(p), nil
	}
	return j.ResponseWriter.Write(p)
}

// requestFromQuery reads a reduction's options from the query string:
// format, budget, preserve, keep (repeatable), key, full, no_store, command
// and exit, as the command line's flags.
func requestFromQuery(r *http.Request) (request, error) {
	v := r.URL.Query()
	q := request{Format: v.Get("format"), Keep: v["keep"], Key: v.Get("key"), Command: v.Get("command")}
	if s := v.Get("budget"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return q, usagef("budget: %q is not a number", s)
		}
		q.Budget = n
	}
	if v.Has("preserve") {
		p := v.Get("preserve")
		q.Preserve = &p
	}
	var err error
	if q.Full, err = boolParam(v.Get("full")); err != nil {
		return q, usagef("full: %v", err)
	}
	if q.NoStore, err = boolParam(v.Get("no_store")); err != nil {
		return q, usagef("no_store: %v", err)
	}
	if s := v.Get("exit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return q, usagef("exit: %q is not a number", s)
		}
		q.Exit = &n
	}
	_, err = q.options()
	return q, err
}

func boolParam(s string) (bool, error) {
	if s == "" {
		return false, nil
	}
	return strconv.ParseBool(s)
}

func wantsText(r *http.Request) bool {
	a := r.Header.Get("Accept")
	return strings.HasPrefix(a, "text/plain")
}

func replyJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSON(w, v)
}

// replyError answers with the status that fits the error.
func replyError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var ue *usageError
	var be *abbreviatory.BudgetError
	var mb *http.MaxBytesError
	var ie *abbreviatory.InputError
	switch {
	case errors.As(err, &ue):
		status = http.StatusBadRequest
	case errors.As(err, &mb):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(err, store.ErrNotFound), errors.Is(err, errNoHistory):
		status = http.StatusNotFound
	case errors.As(err, &ie):
		status = http.StatusBadRequest
		if !strings.HasPrefix(ie.Msg, "JSON:") {
			break
		}
		fallthrough
	case errors.As(err, &be), errors.Is(err, abbreviatory.ErrBinary):
		status = http.StatusUnprocessableEntity
	}
	replyJSON(w, status, map[string]any{"error": err.Error()})
}
