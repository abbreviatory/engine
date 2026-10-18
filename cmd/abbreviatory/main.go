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
	"regexp"
	"strconv"
	"strings"

	abbreviatory "github.com/abbreviatory/engine"
	"github.com/abbreviatory/engine/store"
)

const usage = `abbreviatory: less output, same essential information.

Usage:
  abbreviatory reduce [flags] [file]   reduce a file, or standard input
  abbreviatory expand REF              print what a reference points to
  abbreviatory prune [flags]           remove old stored originals
  abbreviatory formats                 list the input formats
  abbreviatory version                 print the version

Run "abbreviatory reduce -h" for the reduce flags.
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

func cmdReduce(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reduce", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "auto", "input format: auto, test-output, logs, json, or a format from \"abbreviatory formats\"")
	budget := fs.Int("budget", abbreviatory.DefaultBudget, "most bytes of output")
	preserve := fs.String("preserve", "errors,failures,warnings", "what is always shown or referenced (any of errors, failures, warnings; empty for none)")
	var keep multiFlag
	fs.Var(&keep, "keep", "also preserve lines matching this regular expression (repeatable)")
	asJSON := fs.Bool("json", false, "print a JSON object with the counts and the text")
	noStore := fs.Bool("no-store", false, "don't store the original; the output then has no references")
	dir := storeFlag(fs)
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
	in := stdin
	if fs.NArg() == 1 && fs.Arg(0) != "-" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		defer f.Close()
		in = f
	}
	opt := abbreviatory.Options{Format: *format, Budget: *budget, NoRefs: *noStore}
	opt.Preserve = []string{}
	for _, p := range strings.Split(*preserve, ",") {
		if p = strings.TrimSpace(p); p != "" {
			opt.Preserve = append(opt.Preserve, p)
		}
	}
	for _, k := range keep {
		re, err := regexp.Compile(k)
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory: -keep:", err)
			return 2
		}
		opt.Keep = append(opt.Keep, re)
	}
	var pend *store.Pending
	var st *store.Store
	if !*noStore {
		s, err := openStore(*dir)
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		st = s
		pend, err = st.Begin()
		if err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		opt.Sink = pend
	}
	res, err := abbreviatory.Reduce(in, opt)
	if err != nil {
		if pend != nil {
			pend.Abort()
		}
		fmt.Fprintln(stderr, "abbreviatory:", err)
		return 1
	}
	if pend != nil {
		if err := pend.Commit(res.Index); err != nil {
			fmt.Fprintln(stderr, "abbreviatory: storing the original:", err)
			return 1
		}
		st.PruneNow()
	}
	if *asJSON {
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
		if !*noStore {
			out["ref"] = res.ID
		}
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintln(stderr, "abbreviatory:", err)
			return 1
		}
		return 0
	}
	if _, err := io.WriteString(stdout, res.Text); err != nil {
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
