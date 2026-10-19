# Notes for Claude

This file says where the next session starts and what comes next. What comes
next is decided with the owner before it goes here, and work starts only when
the owner says go.

## Releases

Releases are automated. To release, raise `Version` in `abbreviatory.go` and
push to `main`; never ask the owner to publish one. [RELEASING.md](RELEASING.md)
has the procedure and the checks to run afterwards.

## Where things stand

v0.4.0 (October 2026) adds adapters for diffs, HTTP responses and directory
listings, v0.3.0 the HTTP service and the MCP server, v0.2.0 run history (all
below). v0.1.0 was the first
release: Apache 2.0, CI on Linux and macOS plus a Windows smoke check, and automated releases with smoke-tested binaries
for Linux, macOS and Windows. Pure Go, standard library only, no cgo.

The idea: a deterministic context reduction engine. No language model, no
network, no tokens. It turns large tool output into a short report that keeps
failures, errors and warnings by explicit rules, groups repeats, fits a hard
byte budget, and keeps everything it leaves out addressable by reference.

v0.1.0 has the core, the command line and three adapter families:
- `abbreviatory.go`: Options, Result, Index, `Reduce`, the version.
- `record.go`: records, kinds, ranges, `normalize` and `fingerprint` (grouping
  identity: numbers, hex runs and spacing don't count).
- `group.go`: the grouper (one group per fingerprint, capped at 50,000
  distinct per kind, line ranges kept compactly as varints), the index.
- `render.go`: sections, the budget passes (reserve the header, counts and
  the longest possible "not shown" lines; one line per preserved item; detail
  in rounds of 2, 6, 16, 40 lines; other groups), head-and-tail detail.
- `adapter.go`: format table and detection. `gotest.go` (go test -json and
  text), `pytest.go`, `junit.go` (byte ranges, since reports are often one
  line), `logs.go` (levels, stack traces, tracebacks, panics), `jsondoc.go`
  (offset-keeping parser, error fields, outline levels).
- `expand.go`: `ParseRef`, `Expand` and its selectors.
- `store/`: gzipped originals with a JSON index, `Expand`, `Prune`.
- `cmd/abbreviatory/`: reduce, run, history, expand, serve, mcp, prune,
  formats, version.

v0.2.0 adds the run-to-run delta:
- `history.go`: `Run` (what history keeps of a run), items (a failed test by
  name, anything else by its grouping fingerprint or runner ID, JSON error
  fields by pointer and value), `computeDelta` (new, fixed, not run, still
  failing with streaks, flaky: 3+ status changes in the last 10 runs), the
  lines under the header, notes on items, the one-line "no change" report.
  `Reduce` takes `Track`, `History`, `Full`, `Command`, `Exit`; `trackRun`
  fits the changes into the budget (at most a third, lists cut short, then
  only the summary line).
- `store/history.go`: one JSON file per key under `history/`, 50 runs (the
  last 10 full), a lock file, pruned after 90 days unused.
- `cmd`: `reduce -key`, `-full`; `run -- cmd` (spools the output, keeps the
  exit status, default key = project dir + command); `history [KEY]`.
- `delta_test.go`: 500 simulated runs each of go test -v, plain go test
  (two packages with the same test names) and a log, with exit statuses,
  through Reduce and the store, checked against an independent oracle.
- An independent review before v0.2.0 found 8 issues (plain go test and
  pytest never reporting fixed, test names without their package, exit
  status changes folded into "no change", run ignoring signals, concurrent
  runs of one key, run's exit on a failed reduction, docs, passing tests
  past the group cap). All fixed, with tests (history_test.go, store and
  cmd tests). A second pass found 5 more, 3 caused by the fixes (a run
  with no passes made missing tests fixed; tests of a run cut short before
  its package line; signals after the command exited; reduce -key holding
  the lock while reading a pipe; memory of buffered go test results). All
  fixed: Anon only when the adapter shows unnamed passes, matchBare by test
  name, cancellable lock wait, stdin spooled, only failures buffered,
  MaxGood cap. A third pass found the unnamed-pass rule needed to be per
  package (Run.Ran: go packages that ran, not "[build failed]") and shared
  names in cut-short runs; both fixed. Known and documented: pytest without
  -v has no per-module check; a three-waiter stale-lock race (lock files
  carry a token so a run only removes its own). `run` doesn't use process groups on purpose: a signal goes to
  the command, as it would without abbreviatory in front.

v0.3.0 adds the front ends:
- `cmd/abbreviatory/core.go`: the one core all three use: `request` (the
  options, checked; `usageError`), `core.reduce` (store, history under the
  lock, spooling streamed input when there's a key), `outcome.JSON`,
  `expand`, `histories`/`history` and their text and JSON, `execute` (runs
  a command into a spool; signal forwarding for the CLI, process group,
  timeout and cancel for MCP).
- `serve.go`: HTTP API (Go 1.22 ServeMux patterns), loopback unless a
  token, errors mapped to statuses (`abbreviatory.InputError` is new for
  bad references and unreadable input).
- `mcp.go`: JSON-RPC over stdio, tools reduce, run, expand, history; each
  tools/call in its own goroutine; notifications/cancelled stops a run.
- An independent review of 0.3 found: no Host/Origin check without a token
  (DNS rebinding and cross-site requests could read originals and write
  runs), MCP leaving running commands behind on shutdown, expand loading
  whole originals (now `ExpandTo`/`Store.ExpandTo` stream), Windows run
  stopping only the direct process (now taskkill /T), plain-text 404/405,
  case-sensitive Bearer, `/runs/` with an empty key, JSON-RPC batches. All
  fixed with tests; serve also limits concurrent reductions (a slot is taken
  only after the body is spooled, so slow uploads block nobody) and has read
  and idle timeouts. A second pass also fixed the forced kill 5 s after a
  stop hitting a reused PID (single process and Windows), and JSON pointer
  refs with raw slashes (/expand/{ref...}). On MCP shutdown, running commands are stopped but
  every request is still answered; only a client's own cancel drops the
  answer (`mcpCall`).
- `parity_test.go`: the 0.3 bar, every fixture through the CLI, HTTP and
  MCP with five option sets, every reference expanded, histories built
  through each; all the same. Smaller matrix under -short and -race.

v0.4.0 adds three formats:
- `record.go`: `GroupKey` (grouping identity instead of ID, head and
  detail) and `GroupHead` (the head shown for a group of several), `Items`
  (history identities of a record: JSON error fields as pointer and value).
- `diff.go` + `render_diff.go`: git diff/show/log -p, diff -u and -r,
  combined (merge) hunks with a count per parent; commits, files (status,
  +/-, line ref), hunks grouped by a hash of all changed lines (generated
  and binary files apart), conflict markers errors, trailing whitespace
  warnings, OTHER for lines between files.
- `http.go`: curl -i/-v and redirect chains; status lines keyed by code,
  key headers, folded headers and curl notes, curl errors; a JSON body
  parsed (error clusters with Items, the rest one record with the outline
  as detail), other bodies read as a log; render keeps input order for HTTP
  and gives OTHER groups detail. A status line in a body counts only after a
  blank line.
- `listing.go`: find/fd paths, ls -l, ls -R, tree, du; grouped by directory
  and extension; own summary (files, directories, size, extensions,
  largest); tree's own count wins.
- Detection order: http, tests, json, diff, listing, logs.
- An independent review of 0.4 found: diff lines outside files dropped
  uncounted, combined diffs, hunks folded by their first 200 lines,
  generated files swallowing a shared change, HTTP history keyed by
  position and by whole JSON clusters, status lines and curl lines inside
  bodies, pip freeze read as a listing, tree leaf directories. A second
  pass found combined hunks ending in deletions, "@" paths, and notes on
  Items groups. All fixed, each with a test (`review04_test.go`, inputs in
  `testdata/review04`). History follows the innermost error fields
  (`innermost`), for JSON documents too.

The approved plan named a pure-Go SQLite for the store. Cloud sessions can't
download Go modules, so v0.1.0 uses a plain file store instead (the same
behaviour for references; no dependencies). Run history in 0.2 stays in
files too (one JSON file per key); revisit SQLite only if 0.3's service needs
queries files can't do.

The promise, held by `promise_test.go` on every fixture at every budget: the
output never exceeds the budget (or `Reduce` refuses with the size it needs);
every preserved item is shown with its own reference or counted in a "not
shown" line whose reference covers it; same input, same bytes; every
reference expands back byte for byte; for line formats every input line is in
exactly one group. Keep these true.

An independent review before v0.1.0 found 17 bugs (wrong detection of logs
starting with "[", references that changed meaning with -keep, a pytest
count that hung, errors filed as info, unbounded detail, JUnit variants, and
more). All are fixed, each with a test in `review_test.go`. The reference ID
covers the format, the keep rules and the input (`refID`); auto-detected JSON
or JUnit that doesn't parse, or is too big (JSON 16 MB), falls back to logs.

The README's "Limits in v0.1.0" section is the honest list of what isn't
covered; keep it true.

## Plan (approved with the owner, October 2026)

Lead with "what changed since the last run" in CI, not raw token savings; the
agent tool-output space is crowded (RTK, sqz, tokenjuice and others), the run
history and the service position are the difference. The MCP server is one
front end, not the headline.

1. 0.1: core, CLI, three adapters (test output, logs, JSON), store,
   references (done in v0.1.0).
2. 0.2: run-to-run delta, the headline. History keyed by project and command
   (or `-key`). Reports new failures, fixed ones, recurring ones ("failing for
   4 runs") and flaky ones (flipped 3+ times in the last 10 runs). Unchanged
   output reduces to one line. Commands `run -- <command>` (keeps the exit
   code) and `history <key>`. Done when 500 simulated CI runs with known
   changes give an exact delta every time (done in v0.2.0).
3. 0.3: HTTP service (`POST /reduce`, `GET /expand/{ref}`, `GET /runs/{key}`,
   `GET /healthz`; localhost by default, optional bearer token) and an MCP
   server (stdio; tools reduce, run, expand, history). Done when CLI, HTTP and
   MCP give the same results on the whole corpus (done in v0.3.0).
4. 0.4: adapters for Git diffs, HTTP responses and directory listings, each
   passing the same promise tests (done in v0.4.0).

Out of scope: language models, embeddings, summarising prose, log shipping
pipelines, a hosted service.

The browser demo is at abbreviatory.com/demo/ (the owner wants no
subdomain): the engine compiled to WebAssembly (`cmd/abbreviatory-wasm`),
built by `scripts/build-demo.sh` into the site repo's `static/demo/`. Rebuild
and push it to the site after each release.

The approved plan is done with v0.4.0. What comes next is to be decided with
the owner.

## Test results

`sh scripts/record-tests.sh` runs every test, the race detector, the release
plan's cases, a 1 GB memory test and the smoke check on a release-style
binary, and writes them to `test/results/`, with each fixture's reduction in
`test/results/examples/`. Every number on the site and in the README comes
from those files; record again after any change that could move them. It
needs Go and Python 3.

Fixtures: `sh scripts/record-fixtures.sh` re-records the runner fixtures from
the samples in `test/fixtures/src` (they fail on purpose) and regenerates the
log and JSON fixtures (`scripts/gen-fixtures.py`, fixed seed). Re-recording
changes timings in the fixtures; record the test results again afterwards.

## Site

Abbreviatory.com is in `photozeitgeist/abbreviatory.com`: the Hugo Launch
template copied unchanged, with content and config values only. Pages: about,
contact (info@abbreviatory.com), quick start, download, reference. Posts: the
launch, a plain-language explainer of context reduction, how it was tested
(before and after examples from `test/results/examples/`), the name, the 0.2
release (run history), the 0.3 release (MCP server and demo) and the 0.4
release (diffs, HTTP responses, listings). Images are drawn into static/images. Every number comes from `test/results/`.
