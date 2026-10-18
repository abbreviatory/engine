# Abbreviatory reference

This is the full reference for version 0.1. The [README](../README.md) has the
overview and the quick start.

## Commands

```
abbreviatory reduce [flags] [file]   reduce a file, or standard input
abbreviatory expand [flags] REF      print what a reference points to
abbreviatory prune [flags]           remove old stored originals
abbreviatory formats                 list the input formats
abbreviatory version                 print the version
```

Exit status: 0 on success, 1 when the work failed (unreadable or binary
input, a budget too small for this input, an unknown reference), 2 for a usage
mistake (unknown command, format or preserve value, a bad flag or regular
expression, a budget under 512).

### reduce

| Flag | Default | Meaning |
|---|---|---|
| `-format` | `auto` | `auto`, a family (`test-output`, `logs`, `json`) or a format name (below) |
| `-budget` | `4096` | most bytes of output; at least 512 |
| `-preserve` | `errors,failures,warnings` | what is always shown or referenced; empty for nothing |
| `-keep REGEX` | | also preserve lines matching the regular expression, a passing test's output included; repeatable |
| `-json` | off | print a JSON object with counts and the text |
| `-no-store` | off | don't store the original; the output then has no references |
| `-store DIR` | see below | where originals are stored |

With `-json` the object has `version`, `format`, `family`, `ref` (left out with
`-no-store`), `input_bytes`, `input_lines`, `overflow`, `records` (counts by
kind), `lines` (input lines by kind) and `text` (the reduction, within the
budget).

### expand

`abbreviatory expand REF` prints the original bytes a reference stands for.
`REF` is written as it appears in the output, with or without the brackets and
the word `ref`.

### prune

`abbreviatory prune [-older 168h] [-max-size 500MB]` removes stored originals
not used for longer than `-older`, then the least recently used until the store
is no bigger than `-max-size`. `reduce` runs the same pruning with the
defaults at most once an hour.

## Formats

| Name | Family | Detected by |
|---|---|---|
| `gotest-json` | test-output | lines starting `{"Time":`, `{"Action":` or `{"ImportPath":` |
| `gotest` | test-output | `=== RUN`, `--- FAIL:` or `--- PASS:` lines |
| `pytest` | test-output | "test session starts" or a "short test summary info" banner |
| `junit` | test-output | an XML file with `<testsuite>` or `<testcase>` |
| `logs` | logs | anything else that is text |
| `json` | json | a single JSON object or array (not JSON lines) |

`-format test-output` picks the runner, and reads the input as a log if it
finds none. JSON lines (one object per line) are read as a log, with the level
taken from `"level"` or `"severity"`. Detection looks at the first megabyte.
When auto-detection picks `json` or `junit` and the input turns out not to be
one (a log whose lines start with `[`, a damaged report), or is too big to
read whole (16 MB for JSON, 256 MB for JUnit), it is read as a log instead.
With `-format` given, that is an error.

What each format becomes:

- **Test output.** One record per test, with its status. Failed tests keep
  their failure message and output as detail. Go build failures, panics
  outside a test and tests that never finished (the run was killed) are
  reported too. pytest's FAILURES and ERRORS sections are matched to the short
  summary, so each failure carries its traceback; a test that fails and also
  errors in teardown is two records. A pytest crash (`INTERNALERROR>`) is an
  error, and other lines keep their own level. Without `-v`, pytest's pass and
  skip totals come from its final count line. A JUnit report cut off part way
  keeps what was read and reports the unfinished testcase as failed; control
  characters in it are read as `?`.
- **Logs.** One record per line. Indented lines, `Caused by:`, Python
  tracebacks, Go panics with their goroutine dumps, and a Java exception
  printed right after an error line are attached to the line they belong to.
  The level comes from upper-case words near the start of the line (`ERROR`,
  `WARN`, `FATAL`, `CRITICAL` and others), `level=` and JSON level fields,
  bracketed levels, glog prefixes (`E1018 ...`), compiler style `error:` and
  `warning:`, the first line of a panic or traceback, and test results that
  turn up in a log (`--- FAIL:`, `FAIL<tab>`, `FAILED `). An indented line
  with a higher level than the line above it starts its own record.
- **JSON.** An outline of the document that is itself valid JSON. Long arrays
  become their shape, the first elements and every element with an error;
  long strings are cut; deep objects are summarised. Each cut carries a
  reference. Error fields are keys such as `error`, `errors`, `exception`,
  `failure`, `stacktrace` with a non-empty value, and `level`, `severity`,
  `status`, `state` or `outcome` set to `error`, `fatal`, `failed`, `failure`
  or `critical`. A document that fits the budget is shown whole.

## Grouping

Records that differ only in numbers, hex runs (addresses, hashes, request and
trace IDs, UUIDs) and spacing are one group, shown once with a count such as
`x486`. The first record of the group is shown as it was. Records with
different test IDs never group. A stack trace is part of its record, so the
same error with different traces makes separate groups.

A record keeps at most 200 detail lines; the rest are counted. Past 50,000
distinct groups of one kind, further distinct records of that kind
share one catch-all group. Every line stays referenced.

## The output

```
[abbreviatory] <format>, <lines> (<size>), ref <ID>
<counts>
<runner's own summary, if any>
FAILED (n):
- <test> [ref ID:gN]
    <detail lines>
ERRORS (<kinds>, <lines>):
- x<count> <first line of the group> [ref ID:gN]
MATCHED --keep (...):
WARNINGS (...):
OTHER (...):
not shown: <what was left out> [ref ID:<kind>]
```

The order is fixed: failures, errors, keep-rule matches, warnings, then other
groups from the most frequent. Passing and skipped tests are only counted.

How the budget is spent:

1. The header, the counts and the "not shown" lines are reserved first, at
   the longest they could be.
2. Every preserved item gets its one-line entry, in order.
3. Detail lines are handed out in rounds (up to 2 lines each, then 6, 16 and
   40), so every failure gets a few before any gets many. When an item's
   detail doesn't all fit, its first and last lines are shown with a count of
   those in between.
4. Other groups fill what is left.

If the reserved part alone doesn't fit, `reduce` fails with the size it would
need. When an input is so small that showing it whole is no longer than its
reduction, it is shown whole and the header says `shown whole`.

Lines are cleaned for display: ANSI colour codes are removed, other control
characters and invalid UTF-8 become `?`, and lines longer than 300 bytes are
cut with a count of what is left out. The stored original is never changed.

## References

A reference is `ID:selector`. The ID is the first 12 hex digits of a SHA-256
over the format name, the `-keep` rules and the input, the things that decide
how lines are grouped and numbered. The same input with the same rules gives
the same ID, and a reference never points into a different grouping.

| Selector | Expands to |
|---|---|
| (none) | the whole input |
| `gN` | the original lines of group N |
| `fail`, `error`, `warn`, `info`, `pass`, `skip` | every line of that kind |
| `keep` | every line of the groups a `-keep` rule matched |
| `L10` or `L10-40` | input lines 10 to 40 |
| `/items/3` (JSON) | that value as it is in the input (a JSON Pointer, RFC 6901, with each segment percent-encoded in references) |
| `errors` (JSON) | each error field as `pointer<TAB>value` |

Lines come back byte for byte, newlines included, in their input order. A JUnit
report is often one long line, so its groups hold byte ranges: each testcase
element comes back whole, one per line.

## The store

Originals are kept gzipped, each next to a small JSON index of which lines
each group came from. If the original can't be stored (a full disk), the
report is still printed, with a warning that its references won't expand.
The directory is `-store`, else `$ABBREVIATORY_STORE`,
else `abbreviatory` in the user's cache directory (`~/.cache` on Linux,
`~/Library/Caches` on macOS, `%LocalAppData%` on Windows). Nothing leaves the
machine. Use `-no-store` for output you'd rather not keep, such as logs with
secrets.

## Go library

```go
import abbreviatory "github.com/abbreviatory/engine"

res, err := abbreviatory.Reduce(r, abbreviatory.Options{
	Budget:   4096,
	Preserve: []string{"errors", "failures", "warnings"},
	Keep:     []*regexp.Regexp{regexp.MustCompile(`tenant=acme`)},
	Sink:     w, // receives the input as read, to store it
})
fmt.Print(res.Text)

// Later:
part, err := abbreviatory.Expand(original, res.Index, "g12")
```

The `store` package (`github.com/abbreviatory/engine/store`) is the file store
the command uses: `Open`, `Begin`/`Commit` around a `Reduce`, `Expand`,
`Prune`.
