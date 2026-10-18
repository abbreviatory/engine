# Abbreviatory

Less output. Same essential information.

Abbreviatory turns large tool output (a test run, a log, a JSON document) into
a short report. It keeps the failures, errors and warnings, folds repeats into
one line with a count, and fits the result in a byte budget you set. Anything
it leaves out stays one command away, byte for byte.

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
-json            a JSON object with the counts and the text
-no-store        don't keep the original (the report then has no references)
-store DIR       where originals are kept
```

Originals are kept gzipped in your cache directory (or `-store`, or
`$ABBREVIATORY_STORE`) for a week, up to 500 MB, and never leave the machine.
`abbreviatory prune` cleans up by hand.

## In CI

```
go test -json ./... > test.json || status=$?
abbreviatory reduce -budget 3000 test.json >> "$GITHUB_STEP_SUMMARY"
exit ${status:-0}
```

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

Seeded random logs go through the same checks, and every finding of an independent review before the release has its own test in [review_test.go](review_test.go). In all, 52 Go tests pass (with 56 subtests), with the race detector too, on a Linux machine with an Intel Xeon at 2.10GHz and 2 CPUs ([tests.txt](test/results/tests.txt), [race.txt](test/results/race.txt)).

At the default budget ([ratios.txt](test/results/ratios.txt)):

| Fixture | What it is | Input bytes | Input lines | Report bytes | Share |
|---|---|---|---|---|---|
| app.log | generated application log | 5,630,070 | 63,369 | 3,075 | 0.05% |
| gotest-json.txt | go test -json | 808,325 | 4,880 | 2,383 | 0.29% |
| api-response.json | generated API response | 719,368 | 37,527 | 3,185 | 0.44% |
| gotest-verbose.txt | go test -v | 100,652 | 2,453 | 2,256 | 2.2% |
| junit-pytest.xml | pytest's JUnit report | 82,024 | 117 | 3,961 | 4.8% |
| pytest-verbose.txt | pytest -v | 79,443 | 1,072 | 3,893 | 4.9% |
| pytest-short.txt | pytest | 7,479 | 169 | 3,898 | 52% |
| gotest-short.txt | go test | 1,735 | 35 | 1,815 | shown whole |

The last one is small enough that the whole input fits the budget, so it is shown whole with a one-line header.

A generated log of 1,107 MB (12.5 million lines) streamed through in 32.7 seconds with a peak heap of 15.1 MB
([memory.txt](test/results/memory.txt)): for logs and test output, memory
doesn't grow with the input.
Before-and-after reports for each fixture are in
[test/results/examples](test/results/examples).

## Limits in v0.1.0

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
- No run history yet: each reduction stands alone. Comparing a run with the
  last one (new, fixed, recurring and flaky failures) is planned for 0.2, an
  HTTP service and an MCP server for 0.3.

## License

Apache 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE) and
[THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).
