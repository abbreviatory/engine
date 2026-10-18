// Command abbreviatory reduces large tool output (test runs, logs, JSON) to
// a short report that keeps failures, errors and warnings, groups repeats,
// fits a byte budget and keeps everything else addressable by reference.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	abbreviatory "github.com/abbreviatory/engine"
	"github.com/abbreviatory/engine/store"
)

const usage = `abbreviatory: less output, same essential information.

Usage:
  abbreviatory reduce [flags] [file]   reduce a file, or standard input
  abbreviatory run [flags] -- CMD      run a command, reduce its output, compare
                                       with its last run, keep its exit status
  abbreviatory history [KEY]           list run histories, or one history's runs
  abbreviatory expand REF              print what a reference points to
  abbreviatory prune [flags]           remove old stored originals
  abbreviatory formats                 list the input formats
  abbreviatory version                 print the version

Run "abbreviatory reduce -h" or "abbreviatory run -h" for their flags.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "reduce":
		return cmdReduce(args[1:], stdin, stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdin, stdout, stderr)
	case "history":
		return cmdHistory(args[1:], stdout, stderr)
	case "expand":
		return cmdExpand(args[1:], stdout, stderr)
	case "prune":
		return cmdPrune(args[1:], stdout, stderr)
	case "formats":
		fmt.Fprintln(stdout, "auto         detect the format (the default)")
		fmt.Fprintln(stdout, "test-output  a test run: detect the runner, or read it as a log")
		fmt.Fprintln(stdout, "logs         application or terminal logs")
		fmt.Fprintln(stdout, "json         one JSON document")
		for _, f := range abbreviatory.FormatNames() {
			fmt.Fprintln(stdout, f)
		}
		return 0
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, "abbreviatory "+abbreviatory.Version)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "abbreviatory: unknown command %q\n\n%s", args[0], usage)
	return 2
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func storeFlag(fs *flag.FlagSet) *string {
	return fs.String("store", "", "store directory (default $ABBREVIATORY_STORE or the user cache directory)")
}

func openStore(dir string) (*store.Store, error) {
	if dir == "" {
		d, err := store.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	return store.Open(dir)
}

// reduceFlags are the flags reduce and run share.
type reduceFlags struct {
	format, preserve, dir, key string
	budget                     int
	keep                       multiFlag
	asJSON, noStore, full      bool
}

func addReduceFlags(fs *flag.FlagSet) *reduceFlags {
	f := &reduceFlags{}
	fs.StringVar(&f.format, "format", "auto", "input format: auto, test-output, logs, json, or a format from \"abbreviatory formats\"")
	fs.IntVar(&f.budget, "budget", abbreviatory.DefaultBudget, "most bytes of output")
	fs.StringVar(&f.preserve, "preserve", "errors,failures,warnings", "what is always shown or referenced (any of errors, failures, warnings; empty for none)")
	fs.Var(&f.keep, "keep", "also preserve lines matching this regular expression (repeatable)")
	fs.BoolVar(&f.asJSON, "json", false, "print a JSON object with the counts and the text")
	fs.BoolVar(&f.noStore, "no-store", false, "don't store the original; the output then has no references")
	fs.BoolVar(&f.full, "full", false, "with history, print the whole report even when nothing changed since the last run")
	f.dir = ""
	fs.StringVar(&f.dir, "store", "", "store directory (default $ABBREVIATORY_STORE or the user cache directory)")
	return f
}

// options checks the flags and turns them into reduce options. A non-zero
// code means a usage error was printed.
func (f *reduceFlags) options(stderr io.Writer) (abbreviatory.Options, int) {
	if f.budget < abbreviatory.MinBudget {
		fmt.Fprintf(stderr, "abbreviatory: -budget must be at least %d bytes\n", abbreviatory.MinBudget)
		return abbreviatory.Options{}, 2
	}
	if err := abbreviatory.CheckFormat(f.format); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return abbreviatory.Options{}, 2
	}
	opt := abbreviatory.Options{Format: f.format, Budget: f.budget, NoRefs: f.noStore, Full: f.full}
	opt.Preserve = []string{}
	for _, p := range strings.Split(f.preserve, ",") {
		if p = strings.TrimSpace(p); p != "" {
			opt.Preserve = append(opt.Preserve, p)
		}
	}
	if err := abbreviatory.CheckPreserve(opt.Preserve); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return opt, 2
	}
	for _, k := range f.keep {
		re, err := regexp.Compile(k)
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory: -keep:", err)
			return opt, 2
		}
		opt.Keep = append(opt.Keep, re)
	}
	return opt, 0
}

func cmdReduce(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reduce", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := addReduceFlags(fs)
	fs.StringVar(&f.key, "key", "", "keep a run history under this name and report what changed since the last run")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory reduce [flags] [file]\n\nReads the file, or standard input, and prints the reduction.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "abbreviatory reduce: one file at most")
		return 2
	}
	opt, code := f.options(stderr)
	if code != 0 {
		return code
	}
	in := stdin
	if fs.NArg() == 1 && fs.Arg(0) != "-" {
		file, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		defer file.Close()
		in = file
	}
	if f.key != "" && (fs.NArg() == 0 || fs.Arg(0) == "-") {
		// With a history the lock is held from reading the history to
		// writing this run. Read piped input to a file first, so the lock
		// isn't held while whatever writes the pipe is still running.
		sp, err := spoolInput(in)
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		defer os.Remove(sp.Name())
		defer sp.Close()
		in = sp
	}
	if err := reduceAndPrint(in, opt, f, nil, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	return 0
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

// reduceAndPrint reduces in, storing the original and recording the run in
// the history when asked, and prints the report.
// Closing cancel stops a wait for the history's lock; the report is then
// made without a comparison.
func reduceAndPrint(in io.Reader, opt abbreviatory.Options, f *reduceFlags, cancel <-chan struct{}, stdout, stderr io.Writer) error {
	var st *store.Store
	if !f.noStore || f.key != "" {
		s, err := openStore(f.dir)
		if err != nil {
			return err
		}
		st = s
	}
	var pend *store.Pending
	if !f.noStore {
		p, err := st.Begin()
		if err != nil {
			return err
		}
		pend = p
		opt.Sink = pend
	}
	var res *abbreviatory.Result
	reduce := func() error {
		var err error
		res, err = abbreviatory.Reduce(in, opt)
		if err != nil {
			if pend != nil {
				pend.Abort()
			}
			return err
		}
		if pend != nil {
			if err := pend.Commit(res.Index); err != nil {
				// The report is still worth having; say its references
				// won't expand.
				fmt.Fprintln(stderr, "abbreviatory: warning: the original could not be stored, so the references in this report can't be expanded:", err)
			}
		}
		return nil
	}
	if f.key == "" {
		if err := reduce(); err != nil {
			return err
		}
	} else {
		// Compare and record under the history's lock, so a run of the
		// same key at the same time waits and then compares with this one.
		var reduceErr error
		err := st.UpdateCancel(f.key, cancel, func(h *store.History) (*abbreviatory.Run, error) {
			opt.Track = true
			opt.History = h.Runs
			if reduceErr = reduce(); reduceErr != nil {
				return nil, nil
			}
			res.Run.Time = time.Now().UTC().Format(time.RFC3339)
			return res.Run, nil
		})
		if reduceErr != nil {
			return reduceErr
		}
		if err != nil {
			if res == nil {
				// The history couldn't be locked or read: report without it.
				fmt.Fprintln(stderr, "abbreviatory: warning: no comparison with the last run:", err)
				opt.Track, opt.History = false, nil
				if err := reduce(); err != nil {
					return err
				}
			} else {
				fmt.Fprintln(stderr, "abbreviatory: warning: this run could not be added to the history:", err)
			}
		}
	}
	if st != nil {
		st.PruneNow()
	}
	if f.asJSON {
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
		if !f.noStore {
			out["ref"] = res.ID
		}
		if res.Delta != nil {
			out["run"] = res.Run.N
			out["delta"] = deltaJSON(res.Delta)
		}
		if opt.Exit != nil {
			out["exit"] = *opt.Exit
		}
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	_, err := io.WriteString(stdout, res.Text)
	return err
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

func cmdRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	f := addReduceFlags(flags)
	flags.StringVar(&f.key, "key", "", "history name (default: the project directory and the command)")
	noHistory := flags.Bool("no-history", false, "don't keep a run history or compare with the last run")
	flags.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory run [flags] -- command [args]\n\nRuns the command, reduces what it prints (standard output and standard\nerror together), compares the run with the last run of the same command\nand exits with the command's exit status.\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	cmdArgs := flags.Args()
	if len(cmdArgs) == 0 {
		flags.Usage()
		return 2
	}
	opt, code := f.options(stderr)
	if code != 0 {
		return code
	}
	command := strings.Join(cmdArgs, " ")
	opt.Command = command
	if *noHistory {
		f.key = ""
	} else if f.key == "" {
		f.key = defaultKey(command)
	}

	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Stdin = stdin
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		fmt.Fprintf(stderr, "abbreviatory: cannot run %s: %v\n", cmdArgs[0], err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			return 127
		}
		return 126
	}
	w.Close()

	// A signal meant to stop the job (a CI timeout or cancel, Ctrl-C) is
	// passed on to the command; the report is still made from what it
	// printed. A second one kills it.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, stopSignals...)
	forwarded := make(chan struct{})
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

	// The exit status is only known once the command ends, and the report
	// shows it: spool the output to disk first (memory stays flat), then
	// reduce it.
	spool, err := os.CreateTemp("", "abbreviatory-run-*")
	if err != nil {
		io.Copy(io.Discard, r)
		cmd.Wait()
		signal.Stop(sigs)
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return exitOf(cmd, 1)
	}
	defer os.Remove(spool.Name())
	defer spool.Close()
	var written atomic.Int64
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(countingWriter{spool, &written}, r)
		copied <- err
	}()
	waitErr := cmd.Wait()
	status := exitOf(cmd, 1)
	signal.Stop(sigs)
	close(sigs)
	<-forwarded
	if waitErr != nil && cmd.ProcessState == nil {
		fmt.Fprintln(stderr, "abbreviatory:", waitErr)
	}

	// The command is gone. From here a first signal stops any wait for the
	// history's lock (the report comes without a comparison), a second
	// exits with the command's status.
	post := make(chan os.Signal, 2)
	signal.Notify(post, stopSignals...)
	defer signal.Stop(post)
	cancel := make(chan struct{})
	go func() {
		n := 0
		for range post {
			if n++; n == 1 {
				close(cancel)
				continue
			}
			os.Remove(spool.Name())
			os.Exit(status)
		}
	}()

	// Something the command started in the background can keep the output
	// open after the command is gone; reduce what came within a grace
	// period and leave the rest.
	var in io.Reader
	select {
	case err := <-copied:
		r.Close()
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory: reading the command's output:", err)
			return status
		}
		if _, err := spool.Seek(0, io.SeekStart); err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return status
		}
		in = spool
	case <-time.After(outputGrace):
		fmt.Fprintf(stderr, "abbreviatory: the output was still open %s after the command exited (a process it started in the background?); reading stopped\n", outputGrace)
		rf, err := os.Open(spool.Name())
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return status
		}
		defer rf.Close()
		in = io.LimitReader(rf, written.Load())
	}
	opt.Exit = &status
	if err := reduceAndPrint(in, opt, f, cancel, stdout, stderr); err != nil {
		// The command's status decides the exit either way: a CI step
		// shouldn't pass or fail because of the report.
		fmt.Fprintln(stderr, "abbreviatory: could not reduce the output:", err)
	}
	return status
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
// enclosing Git repository, or the current directory) and the command.
func defaultKey(command string) string {
	dir, err := os.Getwd()
	if err != nil {
		return command
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

func cmdHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := storeFlag(fs)
	asJSON := fs.Bool("json", false, "print the history as JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory history [flags] [KEY]\n\nWithout KEY, lists the histories. With KEY (the whole key, or a part\nthat matches one), lists its runs.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	st, err := openStore(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	all, err := st.Histories()
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if fs.NArg() == 0 {
		if *asJSON {
			list := []map[string]any{}
			for _, h := range all {
				m := map[string]any{"key": h.Key, "runs": len(h.Runs)}
				if l := h.Last(); l != nil {
					m["last"], m["time"], m["failing"] = l.N, l.Time, l.Failing
				}
				list = append(list, m)
			}
			return encodeJSON(stdout, stderr, list)
		}
		if len(all) == 0 {
			fmt.Fprintln(stdout, "no histories yet; use reduce -key NAME or run -- COMMAND")
			return 0
		}
		for _, h := range all {
			l := h.Last()
			if l == nil {
				continue
			}
			fmt.Fprintf(stdout, "%s\n    run %d, %s, %s\n", h.Key, l.N, showTime(l.Time), failingWord(l.Failing))
		}
		return 0
	}
	key := fs.Arg(0)
	var match *store.History
	var matches []string
	for _, h := range all {
		if h.Key == key {
			match, matches = h, nil
			break
		}
		if strings.Contains(h.Key, key) {
			match = h
			matches = append(matches, h.Key)
		}
	}
	if len(matches) > 1 {
		fmt.Fprintf(stderr, "abbreviatory: %q matches %d histories:\n", key, len(matches))
		for _, m := range matches {
			fmt.Fprintln(stderr, "  "+m)
		}
		return 1
	}
	if match == nil {
		fmt.Fprintf(stderr, "abbreviatory: no history matches %q\n", key)
		return 1
	}
	if *asJSON {
		runs := []map[string]any{}
		for _, r := range match.Runs {
			m := map[string]any{"run": r.N, "time": r.Time, "ref": r.ID, "format": r.Format, "failing": r.Failing, "new": r.New, "fixed": r.Fixed, "records": r.Records}
			if r.Exit != nil {
				m["exit"] = *r.Exit
			}
			runs = append(runs, m)
		}
		return encodeJSON(stdout, stderr, map[string]any{"key": match.Key, "runs": runs})
	}
	fmt.Fprintln(stdout, match.Key)
	for i := len(match.Runs) - 1; i >= 0; i-- {
		r := match.Runs[i]
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
		line += "  ref " + r.ID
		fmt.Fprintln(stdout, line)
	}
	return 0
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

func encodeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	return 0
}

func cmdExpand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("expand", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := storeFlag(fs)
	fs.Usage = func() {
		fmt.Fprint(stderr, "Usage: abbreviatory expand [flags] REF\n\nPrints what a reference such as 3f2a9c1b7e04:g12 points to: a group's\noriginal lines, all lines of a kind (fail, error, warn, info, pass, skip,\nkeep), a line range (L10-40), the whole input (no selector), or for JSON a\npointer such as /items/3, or errors.\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	st, err := openStore(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	data, err := st.Expand(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		return 1
	}
	return 0
}

func cmdPrune(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := storeFlag(fs)
	older := fs.Duration("older", store.DefaultMaxAge, "remove originals not used for this long")
	maxSize := fs.String("max-size", "500MB", "then remove the least recently used until the store is at most this size")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	limit, err := parseSize(*maxSize)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory: -max-size:", err)
		return 2
	}
	st, err := openStore(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	n, freed, err := st.Prune(*older, limit)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %d stored originals, %s freed\n", n, sizeString(freed))
	return 0
}

func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suf))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("expected a size such as 500MB")
	}
	return n * mult, nil
}

func sizeString(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " kB"
	}
	return strconv.FormatInt(n, 10) + " B"
}
