#!/bin/sh
# Copyright Abbreviatory.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Runs every test, the race detector, the release plan's cases, the large
# input memory test and the smoke check on a release-style binary, and writes
# the output to test/results/ with a note of the machine they ran on. It also
# writes the reduction of every fixture to test/results/examples/. The numbers
# in the README and on abbreviatory.com come from these files.
#
#   sh scripts/record-tests.sh
#
# Needs Go and Python 3. ABBREVIATORY_BIG_MB sets the size of the memory test
# (default 1024).
set -eu
cd "$(dirname "$0")/.."
out=test/results
mkdir -p "$out/examples"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Whether the checkout was clean before anything was recorded.
state="at $(git rev-parse --short HEAD 2>/dev/null || echo 'no commit')$(test -z "$(git status --porcelain 2>/dev/null)" || echo ' plus uncommitted changes')"

header() {
	echo "# $1"
	echo "# recorded $(date -u '+%Y-%m-%d %H:%M UTC') $state, Abbreviatory $(sed -n 's/^const Version = "\(.*\)"$/\1/p' abbreviatory.go), $(go version | cut -d' ' -f3-)"
	cpu=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ //' || true)
	test -n "$cpu" || cpu=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo 'unknown CPU')
	echo "# $(uname -sr), $cpu, $(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo '?') CPUs"
	echo "# command: $2"
	echo
}

go vet ./...

cmd="go test -count=1 -v ./..."
{ header "Every test, verbose" "$cmd"; $cmd 2>&1; } > "$out/tests.txt"

cmd="go test -race -count=1 ./..."
{ header "Every test with the race detector" "$cmd"; $cmd 2>&1; } > "$out/race.txt"

cmd="go test -count=1 -v -run TestRatios ."
{ header "Reduction of each fixture at the default budget (4,096 bytes)" "$cmd"; $cmd 2>&1 | sed -n 's/^.*ratio //p'; } > "$out/ratios.txt"

big=${ABBREVIATORY_BIG_MB:-1024}
cmd="ABBREVIATORY_BIG_MB=$big go test -count=1 -v -run TestLargeInputMemory -timeout 30m ."
{ header "A ${big} MB generated log streamed through Reduce: peak heap and speed" "$cmd"; ABBREVIATORY_BIG_MB=$big go test -count=1 -v -run TestLargeInputMemory -timeout 30m . 2>&1; } > "$out/memory.txt"

cmd="python3 -B -m unittest discover -s scripts -p '*_test.py' -v"
{ header "The release plan's cases" "$cmd"; python3 -B -m unittest discover -s scripts -p '*_test.py' -v 2>&1; } > "$out/release-plan.txt"

# A release-style binary: no cgo, stripped, as the release workflow builds it.
mkdir "$work/bin"
CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$work/bin/abbreviatory" ./cmd/abbreviatory

cmd="python3 scripts/smoke.py BIN"
{ header "The release smoke check on a binary built as the release workflow builds it" "$cmd"; python3 scripts/smoke.py "$work/bin" 2>&1; } > "$out/smoke.txt"

# Before and after: each fixture reduced at the default budget.
for f in test/fixtures/*; do
	test -f "$f" || continue
	name=$(basename "$f")
	"$work/bin/abbreviatory" reduce -store "$work/store" "$f" > "$out/examples/$name.txt"
done

grep -E '^(ok|FAIL|--- FAIL)' "$out/tests.txt" "$out/race.txt" || true
grep 'memory:' "$out/memory.txt" || true
tail -n 1 "$out/release-plan.txt" "$out/smoke.txt"
echo "results written to $out/"
