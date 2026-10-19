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
	"os"
	"os/signal"
	"strconv"
	"strings"

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
  abbreviatory serve [flags]           serve reduce, expand and history over HTTP
  abbreviatory mcp [flags]             serve them to coding agents over MCP (stdio)
  abbreviatory prune [flags]           remove old stored originals
  abbreviatory formats                 list the input formats
  abbreviatory version                 print the version

Run "abbreviatory COMMAND -h" for a command's flags.
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
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "mcp":
		return cmdMCP(args[1:], stdin, stdout, stderr)
	case "prune":
		return cmdPrune(args[1:], stdout, stderr)
	case "formats":
		fmt.Fprint(stdout, formatsText())
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

func formatsText() string {
	var b strings.Builder
	b.WriteString("auto         detect the format (the default)\n")
	b.WriteString("test-output  a test run: detect the runner, or read it as a log\n")
	b.WriteString("logs         application or terminal logs\n")
	b.WriteString("json         one JSON document\n")
	for _, f := range abbreviatory.FormatNames() {
		b.WriteString(f + "\n")
	}
	return b.String()
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func storeFlag(fs *flag.FlagSet) *string {
	return fs.String("store", "", "store directory (default $ABBREVIATORY_STORE or the user cache directory)")
}

// reduceFlags are the flags reduce and run share.
type reduceFlags struct {
	q        request
	preserve string
	keep     multiFlag
	asJSON   bool
	dir      string
}

func addReduceFlags(fs *flag.FlagSet) *reduceFlags {
	f := &reduceFlags{}
	fs.StringVar(&f.q.Format, "format", "auto", "input format: auto, test-output, logs, json, or a format from \"abbreviatory formats\"")
	fs.IntVar(&f.q.Budget, "budget", abbreviatory.DefaultBudget, "most bytes of output")
	fs.StringVar(&f.preserve, "preserve", "errors,failures,warnings", "what is always shown or referenced (any of errors, failures, warnings; empty for none)")
	fs.Var(&f.keep, "keep", "also preserve lines matching this regular expression (repeatable)")
	fs.BoolVar(&f.asJSON, "json", false, "print a JSON object with the counts and the text")
	fs.BoolVar(&f.q.NoStore, "no-store", false, "don't store the original; the output then has no references")
	fs.BoolVar(&f.q.Full, "full", false, "with history, print the whole report even when nothing changed since the last run")
	fs.StringVar(&f.dir, "store", "", "store directory (default $ABBREVIATORY_STORE or the user cache directory)")
	return f
}

// request is the reduction the flags ask for, checked: a usage mistake is
// printed and gives a non-zero code.
func (f *reduceFlags) request(stderr io.Writer) (request, int) {
	q := f.q
	q.Preserve = &f.preserve
	q.Keep = f.keep
	if q.Budget < abbreviatory.MinBudget {
		// Zero means the default elsewhere; on the command line it's a
		// mistake.
		fmt.Fprintf(stderr, "abbreviatory: -budget must be at least %d bytes\n", abbreviatory.MinBudget)
		return q, 2
	}
	if _, err := q.options(); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "keep:") {
			msg = "-" + msg
		}
		fmt.Fprintln(stderr, "abbreviatory:", msg)
		return q, 2
	}
	return q, 0
}

// print prints a reduction as text or JSON, and its warnings.
func (f *reduceFlags) print(o *outcome, stdout, stderr io.Writer) error {
	for _, w := range o.Warnings {
		fmt.Fprintln(stderr, "abbreviatory: warning:", w)
	}
	if f.asJSON {
		return writeJSON(stdout, o.JSON())
	}
	_, err := io.WriteString(stdout, o.Res.Text)
	return err
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdReduce(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reduce", flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := addReduceFlags(fs)
	fs.StringVar(&f.q.Key, "key", "", "keep a run history under this name and report what changed since the last run")
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
	q, code := f.request(stderr)
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
	o, err := core{f.dir}.reduce(in, q, nil)
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if f.print(o, stdout, stderr) != nil {
		return 1
	}
	return 0
}

func cmdRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	f := addReduceFlags(flags)
	flags.StringVar(&f.q.Key, "key", "", "history name (default: the project directory and the command)")
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
	argv := flags.Args()
	if len(argv) == 0 {
		flags.Usage()
		return 2
	}
	q, code := f.request(stderr)
	if code != 0 {
		return code
	}
	q.Command = strings.Join(argv, " ")
	if *noHistory {
		q.Key = ""
	} else if q.Key == "" {
		q.Key = defaultKey("", q.Command)
	}

	e, err := execute(argv, execOpts{Stdin: stdin, Forward: true})
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		var se *startError
		if errors.As(err, &se) {
			return se.Status
		}
		return 1
	}
	defer e.Close()
	for _, w := range e.Warnings {
		fmt.Fprintln(stderr, "abbreviatory:", w)
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
			e.Close()
			os.Exit(e.Status)
		}
	}()

	status := e.Status
	q.Exit = &status
	o, err := core{f.dir}.reduce(e.Output, q, cancel)
	if err != nil {
		// The command's status decides the exit either way: a CI step
		// shouldn't pass or fail because of the report.
		fmt.Fprintln(stderr, "abbreviatory: could not reduce the output:", err)
		return status
	}
	f.print(o, stdout, stderr)
	return status
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
	c := core{*dir}
	if fs.NArg() == 0 {
		all, err := c.histories()
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		if *asJSON {
			return exitJSON(stdout, stderr, historiesJSON(all))
		}
		fmt.Fprint(stdout, historiesText(all))
		return 0
	}
	h, err := c.history(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if *asJSON {
		return exitJSON(stdout, stderr, historyJSON(h))
	}
	fmt.Fprint(stdout, historyText(h))
	return 0
}

func exitJSON(stdout, stderr io.Writer, v any) int {
	if err := writeJSON(stdout, v); err != nil {
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
	data, err := core{*dir}.expand(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		return 1
	}
	return 0
}

func openStore(dir string) (*store.Store, error) { return core{dir}.store() }

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
