# Abbreviatory

Less output. Same essential information.

Abbreviatory turns large tool output (a test run, a log, a JSON document) into
a short report. It keeps the failures, errors and warnings, folds repeats into
one line with a count, and fits the result in a byte budget you set. Anything
it leaves out stays one command away, byte for byte.

Give it a history and it compares each run with the last one: new failures,
fixed ones, those still failing and for how many runs, and flaky ones. When
nothing changed, the report is one line.

It works by rules and pattern matching. No language model, no network, no API
keys. The same input always gives the same output.

```
$ go test -json ./... | abbreviatory reduce
[abbreviatory] go test -json output, 4,880 lines (808.3 kB), ref 8923c60bd06d
tests: 1,204 passed, 7 failed, 1 skipped
FAILED (7):
- TestFieldSpaces (example.com/gosample/parse, 0.00s) [ref 8923c60bd06d:g1205]
    parse_test.go:33: Field("width = 12"): unexpected error: strconv.Atoi: parsing " 12": invalid syntax
    parse_test.go:36: Field("width = 12") = 0, want 12
...
not shown: 1,204 passed tests [ref 8923c60bd06d:pass]

$ abbreviatory expand 8923c60bd06d:g1205
{"Time":"...","Action":"run","Package":"example.com/gosample/parse","Test":"TestFieldSpaces"}
...
```

## Why

Test suites, builds and services print far more than anyone reads. A run with
4,950 passing tests and 50 failures is mostly noise, and when the reader is a
coding agent or a CI job that noise costs time and money on every run.

A summary written by a model can drop the one line that mattered, and you
can't check it. Abbreviatory works the other way. It only removes what its
rules say is safe to remove, it says what it removed, and every removed part
has a reference you can expand. It can't judge free prose; it reads formats it
knows (test runners, logs, JSON) and says so when it falls back to reading
plain lines.

## Install

Download a binary for your system from the
[latest release](https://github.com/abbreviatory/engine/releases/latest):

| System | File |
|---|---|
| Linux, x86-64 | [abbreviatory_linux_amd64.tar.gz](https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_linux_amd64.tar.gz) |
| Linux, ARM64 | [abbreviatory_linux_arm64.tar.gz](https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_linux_arm64.tar.gz) |
| macOS, Apple silicon | [abbreviatory_darwin_arm64.tar.gz](https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_darwin_arm64.tar.gz) |
| macOS, Intel | [abbreviatory_darwin_amd64.tar.gz](https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_darwin_amd64.tar.gz) |
| Windows, x86-64 | [abbreviatory_windows_amd64.zip](https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_windows_amd64.zip) |

Or with Go 1.24 or later:

```
go install github.com/abbreviatory/engine/cmd/abbreviatory@latest
```

It is one static binary with no dependencies.

## Quick start

Pipe output in, or name a file:

```
pytest -v | abbreviatory reduce
abbreviatory reduce build.log
kubectl logs deploy/api | abbreviatory reduce -budget 2000
curl -s https://api.example.com/items | abbreviatory reduce
```

The format is detected: go test (plain or `-json`), pytest, JUnit XML (Jest,
Maven, Gradle and most CI tools write it), application logs, and JSON
documents. Anything else is read as a log. `-format` picks one by hand.

Each line of the report that stands for more than itself ends with a
reference. Expand it to get the original back:

```
abbreviatory expand d2f86a61aa96:g10      # one group: an error and its repeats
abbreviatory expand d2f86a61aa96:error    # every error line
abbreviatory expand d2f86a61aa96:L200-260 # a line range
abbreviatory expand d2f86a61aa96          # the whole input
```

## What changed since the last run

Name a history with `-key`, or let `run` name it after the project and the
command, and each report starts with what changed:

```
$ go test -json ./... | abbreviatory reduce -key unit
[abbreviatory] go test -json output, 4,882 lines (808.5 kB), ref 3e35712ad529
run 2; since run 1: 1 new, 1 fixed, 6 still failing (longest for 2 runs)
new: TestFieldSpaces (example.com/gosample/parse, 0.00s) [ref 3e35712ad529:g1205]
fixed: TestLegacyFormat (example.com/gosample/parse, 0.00s) [ref 6f92e70f603c:g1213]
tests: 1,205 passed, 7 failed, 1 skipped
FAILED (7):
- TestFieldSpaces (example.com/gosample/parse, 0.00s) (new) [ref 3e35712ad529:g1205]
    parse_test.go:33: Field("width = 12"): unexpected error: strconv.Atoi: parsing " 12": invalid syntax
    parse_test.go:36: Field("width = 12") = 0, want 12
- TestPickPast (example.com/gosample/crash, 0.00s) (failing for 2 runs) [ref 3e35712ad529:g1]
...
```

The same run again:

```
[abbreviatory] go test -json output, 4,882 lines (808.5 kB), ref 3e35712ad529; run 3, identical to run 2: the same 7 failures
```

- **New** failures come first in the list, marked `(new)`.
- **Fixed** ones are listed with a reference into the run where they last
  failed, so the old failure is still one `expand` away. A failing test that
  doesn't appear at all this time is listed as **not run**, not fixed.
- **Still failing** ones are marked with how many runs in a row they have
  failed.
- **Flaky** ones changed between passing and failing at least 3 times in the
  last 10 runs. They are listed as flaky instead of new or fixed, so a flaky
  test doesn't look like news every other run.
- **No change** (the same failures as last time, timings and IDs aside) is one
  line. `-full` prints the whole report anyway.

For logs the same works on error kinds: new, gone, recurring and intermittent.
`run` runs the command, reduces what it prints (standard output and standard
error together) and exits with the command's exit status, so it can stand in
for the command in CI. A CI timeout or cancel that signals `run` reaches the
command too, and the report is still printed. A change in exit status alone
(a job killed after the same output) is reported, not folded into "no
change". `abbreviatory history` lists the runs.

## What the report holds

A log of 63,369 lines (5.6 MB) at the default budget:

```
[abbreviatory] log, 63,369 lines (5.6 MB), ref d2f86a61aa96
lines: 4,313 error, 3,252 warn, 55,804 other; 14 distinct messages
ERRORS (4 kinds, 4,313 lines):
- x264 2026-10-18T08:00:07.073Z ERROR checkout failed for order 75504 [ref d2f86a61aa96:g9]
    java.lang.IllegalStateException: payment gateway did not answer
    	at com.example.orders.PaymentClient.charge(PaymentClient.java:118)
    ...
- x486 2026-10-18T08:00:10.517Z ERROR db: connection to 10.0.4.5:5432 refused (request_id=b05ab8a9) [ref d2f86a61aa96:g10]
...
WARNINGS (3 kinds, 3,252 lines):
- x1952 2026-10-18T08:00:00.074Z WARN  config key "timeout" is deprecated; use "read_timeout" [ref d2f86a61aa96:g2]
...
OTHER (7 kinds, 55,804 lines):
- x14860 2026-10-18T08:00:00.037Z DEBUG cache hit key=user:59944 ttl=515s [ref d2f86a61aa96:g1]
...
```

- **Repeats fold.** Lines that differ only in numbers, IDs, addresses and
  timestamps are one group, shown once with a count. A stack trace stays with
  its line, so the same error with a different trace is a different group.
- **Preserved items come first.** Failures, then errors, then lines matching
  your `-keep` rules, then warnings. Each gets a line before any gets detail,
  and detail is shared out in rounds so every failure shows some of its
  message.
- **The budget is hard.** The report never goes over `-budget` bytes (4,096
  by default). If a preserved item doesn't fit, a "not shown" line counts it
  and its reference brings it back. If the budget is too small even for
  that, `reduce` says how much it needs and prints nothing.
- **References are exact.** A reference's ID covers the input, its format
  and your `-keep` rules, so it always points into the reduction that printed
  it.
- **Small inputs pass through.** When showing the input whole would be no
  longer than reducing it, you get it whole.

## Options

```
-budget N        most bytes of output (default 4096, at least 512)
-preserve LIST   errors,failures,warnings by default; empty for none
-keep REGEX      also keep lines matching this (repeatable)
-format NAME     auto, test-output, logs, json, gotest-json, gotest, pytest, junit
-key NAME        keep a run history under NAME and report what changed
-full            with history, the whole report even when nothing changed
-json            a JSON object with the counts, the changes and the text
-no-store        don't keep the original (the report then has no references)
-store DIR       where originals and histories are kept
```

`run` takes the same flags, names its history after the project (the
enclosing Git repository, or the directory) and the command unless `-key` is
given, and has `-no-history`.

Originals are kept gzipped in your cache directory (or `-store`, or
`$ABBREVIATORY_STORE`) for a week, up to 500 MB, and never leave the machine.
Histories keep the last 50 runs of each key and go after 90 days unused.
`abbreviatory prune` cleans up by hand.

## In CI

CI machines start empty, so the store has to be kept between runs for the
history to work. On GitHub Actions, with the cache saved whether the tests
pass or not:

```yaml
- uses: actions/cache/restore@v4
  with:
    path: .abbreviatory
    key: abbreviatory-${{ github.ref_name }}-${{ github.run_id }}
    restore-keys: abbreviatory-${{ github.ref_name }}-
- run: abbreviatory run -store .abbreviatory -key unit -budget 3000 -- go test -json ./... >> "$GITHUB_STEP_SUMMARY"
- if: always()
  run: abbreviatory prune -store .abbreviatory -max-size 50MB
- if: always()
  uses: actions/cache/save@v4
  with:
    path: .abbreviatory
    key: abbreviatory-${{ github.ref_name }}-${{ github.run_id }}
```

The step fails when the tests fail, because `run` exits with their status.

## As a Go library

```go
res, err := abbreviatory.Reduce(r, abbreviatory.Options{Budget: 4096})
fmt.Print(res.Text)
```

`go get github.com/abbreviatory/engine`. The [reference](docs/reference.md)
covers the API, every format, the selectors and the exact layout of the
report.

## How it's tested

Every number here comes from [test/results](test/results), written by
`scripts/record-tests.sh`.

The fixtures are real output: go test and pytest runs of the small samples in
`test/fixtures/src` (they fail on purpose), pytest's JUnit report, plus a
generated application log and API response. On every fixture, at budgets from
256 bytes to 16 kB, with several preserve settings and keep rules,
[promise_test.go](promise_test.go) checks that:

- the output stays within the budget, or `reduce` refuses with the size it needs;
- every failure, error, warning and keep-rule match is shown with its own
  reference, or counted in a "not shown" line whose reference covers it;
- two runs give the same bytes;
- every reference expands back to exactly the original bytes, and for line
  formats every input line belongs to exactly one group.

Seeded random logs go through the same checks, and every finding of the independent reviews before v0.1.0 and v0.2.0 has its own test ([review_test.go](review_test.go), [history_test.go](history_test.go)). In all, 69 Go tests pass (with 59 subtests), with the race detector too, on a Linux machine with an Intel Xeon at 2.10GHz and 2 CPUs ([tests.txt](test/results/tests.txt), [race.txt](test/results/race.txt)).

At the default budget ([ratios.txt](test/results/ratios.txt)):

| Fixture | What it is | Input bytes | Input lines | Report bytes | Share |
|---|---|---|---|---|---|
| app.log | generated application log | 5,630,070 | 63,369 | 3,075 | 0.05% |
| gotest-json.txt | go test -json | 808,325 | 4,880 | 2,383 | 0.29% |
| api-response.json | generated API response | 719,368 | 37,527 | 3,185 | 0.44% |
| gotest-verbose.txt | go test -v | 100,652 | 2,453 | 2,449 | 2.4% |
| junit-pytest.xml | pytest's JUnit report | 82,024 | 117 | 3,961 | 4.8% |
| pytest-verbose.txt | pytest -v | 79,443 | 1,072 | 3,893 | 4.9% |
| pytest-short.txt | pytest | 7,479 | 169 | 3,898 | 52% |
| gotest-short.txt | go test | 1,735 | 35 | 1,815 | shown whole |

The last one is small enough that the whole input fits the budget, so it is shown whole with a one-line header.

A generated log of 1,107 MB (12.5 million lines) streamed through in 32.3 seconds with a peak heap of 12.1 MB
([memory.txt](test/results/memory.txt)): for logs and test output, memory
doesn't grow with the input.
Before-and-after reports for each fixture are in
[test/results/examples](test/results/examples), with a run-to-run example.

The history was checked on 500 simulated CI runs each of a 40-test suite
printed by `go test -v`, the same suite by plain `go test`, and a log with 25
error kinds: tests breaking, getting fixed, flaking, being skipped and going
missing at random, the same test names in two packages, exit statuses
changing, and some runs changing nothing ([delta_test.go](delta_test.go)). Each run went through the whole command
path, its history written to the store and read back for the next run, at a
random budget. For every run the reported new, fixed, not run, still
failing and flaky items, the streaks, the "no change" line and the notes in
the report matched what was worked out separately from the known statuses
([delta.txt](test/results/delta.txt)).

## Limits in v0.2.0

- It reads formats it knows. Free prose, or a log format it doesn't
  recognise, is reduced only by folding repeats and finding level words.
- Grouping treats numbers as unimportant. Two errors that differ only in a
  number (a port, a status code) are one group; the first is shown and the
  count says how many.
- JSON documents are read whole, up to 16 MB, and JUnit reports up to
  256 MB. A JSON document takes many times its size in memory while it is
  read. Past those sizes, or when something looked like JSON and wasn't, the
  input is read as a log. Logs and test output stream with no size limit,
  and their memory use doesn't grow with the input.
- In a JSON document with a repeated key, a reference to the second one
  expands to the first.
- Lines end at newlines. Progress output that redraws one line with carriage
  returns (download bars) counts as one long line. Text in UTF-16 is refused
  as binary.
- References need the stored original. With `-no-store`, or after pruning,
  they can't be expanded.
- History compares failures and errors only: warnings and keep-rule matches
  aren't tracked. A test is known by its name, anything else by its text
  with numbers and IDs aside, so a log error whose wording changes counts as
  a new one, and the same error with a different stack trace too.
- In a JSON document, error fields are compared by their path and value with
  array positions aside, so the same error at two positions counts once.
- When the output doesn't name passing tests (go test or pytest without
  `-v`), a failing test that disappears counts as fixed, even if it was
  deleted or not run. With `-v` or `-json` it counts as not run. For go
  test this applies per package: a package that failed to build leaves its
  tests not run. pytest without `-v` has no such per-module check, so a
  test in a module that failed to import counts as fixed.
- `run` signals the command, not the processes it started. With a history,
  a test suite of a million tests takes a few hundred MB while it is
  compared (about 225 MB for 750,000 tests in one package).
- Flaky detection needs the last 10 runs; a history trimmed or started fresh
  sees nothing as flaky at first.
- No HTTP service or MCP server yet; they are planned for 0.3.

## License

Apache 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
