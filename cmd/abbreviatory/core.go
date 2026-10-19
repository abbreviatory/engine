package main

// The core every front end shares: the command line, the HTTP service and
// the MCP server all reduce, expand, list histories and run commands through
// these functions, so they give the same results.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
	"github.com/abbreviatory/engine/store"
)

// request is one reduction, however it was asked for.
type request struct {
	Format   string
	Budget   int
	Preserve *string // nil means "errors,failures,warnings"; "" means nothing
	Keep     []string
	NoStore  bool
	Full     bool
	Key      string // a history to compare with and record in
	Command  string // the command that printed the input, when known
	Exit     *int   // its exit status
}

// usageError is a mistake in the request itself: the command line exits 2,
// the HTTP service answers 400, the MCP server returns a tool result marked
// as an error.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// options checks the request and turns it into reduce options.
func (q request) options() (abbreviatory.Options, error) {
	if q.Format == "" {
		q.Format = "auto"
	}
	if q.Budget == 0 {
		q.Budget = abbreviatory.DefaultBudget
	}
	if q.Budget < abbreviatory.MinBudget {
		return abbreviatory.Options{}, usagef("budget must be at least %d bytes", abbreviatory.MinBudget)
	}
	if err := abbreviatory.CheckFormat(q.Format); err != nil {
		return abbreviatory.Options{}, &usageError{err.Error()}
	}
	opt := abbreviatory.Options{Format: q.Format, Budget: q.Budget, NoRefs: q.NoStore, Full: q.Full, Command: q.Command, Exit: q.Exit}
	preserve := "errors,failures,warnings"
	if q.Preserve != nil {
		preserve = *q.Preserve
	}
	opt.Preserve = []string{}
	for _, p := range strings.Split(preserve, ",") {
		if p = strings.TrimSpace(p); p != "" {
			opt.Preserve = append(opt.Preserve, p)
		}
	}
	if err := abbreviatory.CheckPreserve(opt.Preserve); err != nil {
		return opt, &usageError{err.Error()}
	}
	for _, k := range q.Keep {
		re, err := regexp.Compile(k)
		if err != nil {
			return opt, usagef("keep: %v", err)
		}
		opt.Keep = append(opt.Keep, re)
	}
	return opt, nil
}

// outcome is a finished reduction with what went wrong on the side.
type outcome struct {
	Res      *abbreviatory.Result
	NoStore  bool
	Exit     *int
	Warnings []string
}

// JSON is the reduction as every front end reports it in JSON.
func (o *outcome) JSON() map[string]any {
	res := o.Res
	out := map[string]any{
		"version":     abbreviatory.Version,
		"format":      res.Format,
		"family":      res.Family,
		"input_bytes": res.InputBytes,
		"input_lines": res.InputLines,
		"overflow":    res.Overflow,
		"records":     res.Records,
		"lines":       res.Lines,
		"text":        res.Text,
	}
	if !o.NoStore {
		out["ref"] = res.ID
	}
	if res.Delta != nil {
		out["run"] = res.Run.N
		out["delta"] = deltaJSON(res.Delta)
	}
	if o.Exit != nil {
		out["exit"] = *o.Exit
	}
	if len(o.Warnings) > 0 {
		out["warnings"] = o.Warnings
	}
	return out
}

func deltaJSON(d *abbreviatory.Delta) map[string]any {
	items := func(list []abbreviatory.DeltaItem) []map[string]any {
		out := []map[string]any{}
		for _, it := range list {
			m := map[string]any{"head": it.Head, "ref": it.Ref}
			if it.Flips > 0 {
				m["now"], m["changes"], m["window"] = it.Now, it.Flips, it.Window
			}
			out = append(out, m)
		}
		return out
	}
	return map[string]any{
		"since":     d.Since,
		"identical": d.Identical,
		"unchanged": d.Unchanged,
		"failing":   d.Failing,
		"new":       items(d.New),
		"fixed":     items(d.Fixed),
		"not_run":   items(d.NotRun),
		"flaky":     items(d.Flaky),
		"still":     d.Still,
		"longest":   d.Longest,
	}
}

// core holds where the store is.
type core struct {
	dir string // the store directory; empty for the default
}

func (c core) store() (*store.Store, error) {
	dir := c.dir
	if dir == "" {
		d, err := store.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return store.Open(dir)
}

// reduce reduces in, storing the original and recording the run in the
// history when asked. Closing cancel stops a wait for the history's lock;
// the report is then made without a comparison.
func (c core) reduce(in io.Reader, q request, cancel <-chan struct{}) (*outcome, error) {
	opt, err := q.options()
	if err != nil {
		return nil, err
	}
	o := &outcome{NoStore: q.NoStore, Exit: q.Exit}
	var st *store.Store
	if !q.NoStore || q.Key != "" {
		if st, err = c.store(); err != nil {
			return nil, err
		}
	}
	if q.Key != "" && !isRegularFile(in) {
		// With a history the lock is held from reading the history to
		// writing this run. Read streamed input to a file first, so the
		// lock isn't held while whatever writes the stream still runs.
		sp, err := spoolInput(in)
		if err != nil {
			return nil, err
		}
		defer os.Remove(sp.Name())
		defer sp.Close()
		in = sp
	}
	var pend *store.Pending
	if !q.NoStore {
		if pend, err = st.Begin(); err != nil {
			return nil, err
		}
		opt.Sink = pend
	}
	reduce := func() error {
		res, err := abbreviatory.Reduce(in, opt)
		if err != nil {
			if pend != nil {
				pend.Abort()
			}
			return err
		}
		o.Res = res
		if pend != nil {
			if err := pend.Commit(res.Index); err != nil {
				// The report is still worth having; say its references
				// won't expand.
				o.Warnings = append(o.Warnings, "the original could not be stored, so the references in this report can't be expanded: "+err.Error())
			}
		}
		return nil
	}
	if q.Key == "" {
		if err := reduce(); err != nil {
			return nil, err
		}
	} else {
		// Compare and record under the history's lock, so a run of the
		// same key at the same time waits and then compares with this one.
		var reduceErr error
		err := st.UpdateCancel(q.Key, cancel, func(h *store.History) (*abbreviatory.Run, error) {
			opt.Track = true
			opt.History = h.Runs
			if reduceErr = reduce(); reduceErr != nil {
				return nil, nil
			}
			o.Res.Run.Time = time.Now().UTC().Format(time.RFC3339)
			return o.Res.Run, nil
		})
		if reduceErr != nil {
			return nil, reduceErr
		}
		if err != nil {
			if o.Res == nil {
				// The history couldn't be locked or read: report without it.
				o.Warnings = append(o.Warnings, "no comparison with the last run: "+err.Error())
				opt.Track, opt.History = false, nil
				if err := reduce(); err != nil {
					return nil, err
				}
			} else {
				o.Warnings = append(o.Warnings, "this run could not be added to the history: "+err.Error())
			}
		}
	}
	if st != nil {
		st.PruneNow()
	}
	return o, nil
}

func isRegularFile(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode().IsRegular()
}

// spoolInput copies in to a temporary file and returns it, rewound.
func spoolInput(in io.Reader) (*os.File, error) {
	sp, err := os.CreateTemp("", "abbreviatory-in-*")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(sp, in); err != nil {
		sp.Close()
		os.Remove(sp.Name())
		return nil, err
	}
	if _, err := sp.Seek(0, io.SeekStart); err != nil {
		sp.Close()
		os.Remove(sp.Name())
		return nil, err
	}
	return sp, nil
}

// expand writes what a reference points to, streamed from the store.
func (c core) expand(w io.Writer, ref string) error {
	st, err := c.store()
	if err != nil {
		return err
	}
	return st.ExpandTo(w, ref)
}

// errTooLarge is returned by a limitWriter past its limit.
var errTooLarge = errors.New("too large")

// limitWriter buffers at most n bytes.
type limitWriter struct {
	buf []byte
	n   int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if len(l.buf)+len(p) > l.n {
		return 0, errTooLarge
	}
	l.buf = append(l.buf, p...)
	return len(p), nil
}

// errNoHistory is returned when no history matches a key.
var errNoHistory = errors.New("no history matches")

// histories lists every history, most recently run first.
func (c core) histories() ([]*store.History, error) {
	st, err := c.store()
	if err != nil {
		return nil, err
	}
	return st.Histories()
}

// history finds the history whose key is key or, failing that, the one key
// that contains key.
func (c core) history(key string) (*store.History, error) {
	all, err := c.histories()
	if err != nil {
		return nil, err
	}
	var match *store.History
	var matches []string
	for _, h := range all {
		if h.Key == key {
			return h, nil
		}
		if strings.Contains(h.Key, key) {
			match = h
			matches = append(matches, h.Key)
		}
	}
	if len(matches) > 1 {
		return nil, usagef("%q matches %d histories:\n  %s", key, len(matches), strings.Join(matches, "\n  "))
	}
	if match == nil {
		return nil, fmt.Errorf("%w %q", errNoHistory, key)
	}
	return match, nil
}

func historiesJSON(all []*store.History) []map[string]any {
	list := []map[string]any{}
	for _, h := range all {
		m := map[string]any{"key": h.Key, "runs": len(h.Runs)}
		if l := h.Last(); l != nil {
			m["last"], m["time"], m["failing"] = l.N, l.Time, l.Failing
		}
		list = append(list, m)
	}
	return list
}

func historiesText(all []*store.History) string {
	if len(all) == 0 {
		return "no histories yet; use reduce -key NAME or run -- COMMAND\n"
	}
	var b strings.Builder
	for _, h := range all {
		if l := h.Last(); l != nil {
			fmt.Fprintf(&b, "%s\n    run %d, %s, %s\n", h.Key, l.N, showTime(l.Time), failingWord(l.Failing))
		}
	}
	return b.String()
}

func historyJSON(h *store.History) map[string]any {
	runs := []map[string]any{}
	for _, r := range h.Runs {
		m := map[string]any{"run": r.N, "time": r.Time, "ref": r.ID, "format": r.Format, "failing": r.Failing, "new": r.New, "fixed": r.Fixed, "records": r.Records}
		if r.Exit != nil {
			m["exit"] = *r.Exit
		}
		runs = append(runs, m)
	}
	return map[string]any{"key": h.Key, "runs": runs}
}

func historyText(h *store.History) string {
	var b strings.Builder
	b.WriteString(h.Key + "\n")
	for i := len(h.Runs) - 1; i >= 0; i-- {
		r := h.Runs[i]
		line := fmt.Sprintf("run %d  %s  %s", r.N, showTime(r.Time), failingWord(r.Failing))
		var changes []string
		if r.New > 0 {
			changes = append(changes, fmt.Sprintf("%d new", r.New))
		}
		if r.Fixed > 0 {
			changes = append(changes, fmt.Sprintf("%d fixed", r.Fixed))
		}
		if len(changes) > 0 {
			line += " (" + strings.Join(changes, ", ") + ")"
		}
		if r.Exit != nil {
			line += fmt.Sprintf("  exit %d", *r.Exit)
		}
		b.WriteString(line + "  ref " + r.ID + "\n")
	}
	return b.String()
}

func failingWord(n int) string {
	switch n {
	case 0:
		return "nothing failing"
	case 1:
		return "1 failing"
	}
	return strconv.Itoa(n) + " failing"
}

func showTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "(no time)"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

// execOpts say how to run a command.
type execOpts struct {
	Dir     string
	Stdin   io.Reader
	Timeout time.Duration // 0 for none
	// Forward passes stop signals sent to this process on to the command
	// (the command line); the MCP server leaves them alone.
	Forward bool
	// Cancel, when closed, stops the command (an MCP client cancelled).
	Cancel <-chan struct{}
	// Group starts the command in a process group of its own, and a
	// timeout or a cancel stops the whole group (the MCP server, where no
	// terminal is involved).
	Group bool
}

// executed is a command that ran: its status and its output, spooled.
type executed struct {
	Status   int
	Output   io.Reader
	Warnings []string
	spool    *os.File
	extra    io.Closer
}

// Close removes the spooled output.
func (e *executed) Close() {
	if e.extra != nil {
		e.extra.Close()
	}
	if e.spool != nil {
		e.spool.Close()
		os.Remove(e.spool.Name())
	}
}

// startError is a command that couldn't be started; Status is 127 when it
// wasn't found, else 126.
type startError struct {
	Status int
	Err    error
	Name   string
}

func (e *startError) Error() string { return "cannot run " + e.Name + ": " + e.Err.Error() }

// execute runs argv with standard output and standard error going into one
// pipe, spools what it prints to disk (memory stays flat; the exit status is
// only known at the end) and returns it with the exit status.
func execute(argv []string, x execOpts) (*executed, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = x.Dir
	cmd.Stdin = x.Stdin
	cmd.Stdout = w
	cmd.Stderr = w
	if x.Group {
		ownGroup(cmd)
	}
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		status := 126
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			status = 127
		}
		return nil, &startError{Status: status, Err: err, Name: argv[0]}
	}
	w.Close()

	var forwarded chan struct{}
	var sigs chan os.Signal
	if x.Forward {
		// A signal meant to stop the job (a CI timeout or cancel, Ctrl-C)
		// is passed on to the command; the report is still made from what
		// it printed. A second one kills it.
		sigs = make(chan os.Signal, 2)
		signal.Notify(sigs, stopSignals...)
		forwarded = make(chan struct{})
		go func() {
			defer close(forwarded)
			n := 0
			for sig := range sigs {
				if n++; n == 1 && cmd.Process.Signal(sig) == nil {
					continue
				}
				cmd.Process.Kill()
			}
		}()
	}
	// The second, forceful step of stopping a single process is skipped once
	// it has exited: its process ID may already belong to something else.
	var exited atomic.Bool
	stop := func() {
		if x.Group {
			if stopGroup(cmd, false) != nil {
				stopGroup(cmd, true)
				return
			}
			if runtime.GOOS == "windows" {
				return // taskkill /F has ended the tree already
			}
			// The group's ID stays taken while any of it runs, so this also
			// reaches what the command left behind ignoring SIGTERM.
			time.AfterFunc(5*time.Second, func() { stopGroup(cmd, true) })
			return
		}
		if cmd.Process.Signal(os.Interrupt) != nil {
			cmd.Process.Kill()
			return
		}
		time.AfterFunc(5*time.Second, func() {
			if !exited.Load() {
				cmd.Process.Kill()
			}
		})
	}
	if x.Cancel != nil {
		finished := make(chan struct{})
		defer close(finished)
		go func() {
			select {
			case <-x.Cancel:
				stop()
			case <-finished:
			}
		}()
	}
	var timedOut atomic.Bool
	if x.Timeout > 0 {
		t := time.AfterFunc(x.Timeout, func() {
			timedOut.Store(true)
			stop()
		})
		defer t.Stop()
	}

	e := &executed{}
	spool, err := os.CreateTemp("", "abbreviatory-run-*")
	if err != nil {
		io.Copy(io.Discard, r)
		cmd.Wait()
		if x.Forward {
			signal.Stop(sigs)
			close(sigs)
			<-forwarded
		}
		return nil, err
	}
	e.spool = spool
	var written atomic.Int64
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(countingWriter{spool, &written}, r)
		copied <- err
	}()
	waitErr := cmd.Wait()
	exited.Store(true)
	e.Status = exitOf(cmd, 1)
	if x.Forward {
		signal.Stop(sigs)
		close(sigs)
		<-forwarded
	}
	if waitErr != nil && cmd.ProcessState == nil {
		e.Warnings = append(e.Warnings, waitErr.Error())
	}
	if timedOut.Load() {
		e.Warnings = append(e.Warnings, "the command ran past its time limit of "+x.Timeout.String()+" and was stopped")
	}

	// Something the command started in the background can keep the output
	// open after the command is gone; take what came within a grace period
	// and leave the rest.
	select {
	case err := <-copied:
		r.Close()
		if err != nil {
			e.Close()
			return nil, fmt.Errorf("reading the command's output: %w", err)
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			e.Close()
			return nil, err
		}
		e.Output = spool
	case <-time.After(outputGrace):
		e.Warnings = append(e.Warnings, fmt.Sprintf("the output was still open %s after the command exited (a process it started in the background?); reading stopped", outputGrace))
		rf, err := os.Open(spool.Name())
		if err != nil {
			e.Close()
			return nil, err
		}
		e.extra = rf
		e.Output = io.LimitReader(rf, written.Load())
	}
	return e, nil
}

// countingWriter counts what it writes.
type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// outputGrace is how long run keeps reading output after the command exits.
var outputGrace = 5 * time.Second

// stopSignals are the signals run passes on to the command.
var stopSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// exitOf is the command's exit status: its own code, 128 plus the signal
// number when a signal ended it, or def if it never ran.
func exitOf(cmd *exec.Cmd, def int) int {
	ps := cmd.ProcessState
	if ps == nil {
		return def
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return def
}

// defaultKey names the history of a command: the project it runs in (the
// enclosing Git repository, or dir itself) and the command.
func defaultKey(dir, command string) string {
	if dir == "" {
		d, err := os.Getwd()
		if err != nil {
			return command
		}
		dir = d
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	project := dir
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			project = d
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return project + ": " + command
}
