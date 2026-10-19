#!/bin/sh
# Copyright Abbreviatory.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Records the test-runner fixtures from the sample projects in
# test/fixtures/src: real go test output (JSON, verbose, plain), real pytest
# output (verbose, plain) and pytest's JUnit XML report. The samples fail on
# purpose. Then writes the generated log and JSON fixtures, a real git
# diff, real HTTP responses from curl and real directory listings.
#
#   sh scripts/record-fixtures.sh
#
# Needs Go, Python 3, pytest, git (with this repository's history) and curl. Timings and addresses in the output change
# from run to run, so re-recording changes the fixtures; record the test
# results again afterwards.
set -u
cd "$(dirname "$0")/.."
f=$(pwd)/test/fixtures
(
	cd test/fixtures/src/gosample
	go test -json ./... > "$f/gotest-json.txt" 2>&1
	go test -v ./... > "$f/gotest-verbose.txt" 2>&1
	go test ./... > "$f/gotest-short.txt" 2>&1
)
(
	cd test/fixtures/src/pysample
	pytest -v -p no:cacheprovider test_sample.py > "$f/pytest-verbose.txt" 2>&1
	pytest -p no:cacheprovider test_sample.py > "$f/pytest-short.txt" 2>&1
	pytest -q -p no:cacheprovider --junitxml="$f/junit-pytest.xml" test_sample.py > /dev/null 2>&1
)
python3 scripts/gen-fixtures.py

# A real git diff: the 0.3 work, code, docs and recorded test results.
git diff 2670f3a adbafa2 > "$f/git-diff.txt"

# Real HTTP responses: curl against Python's web server, serving the
# fixtures directory: a JSON document, a redirect to a directory listing
# page, and a 404 with curl -v's own lines.
port=18731
(cd "$f" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) &
server=$!
sleep 1
curl -si "http://127.0.0.1:$port/api-response.json" > "$f/http-json.txt"
curl -siL "http://127.0.0.1:$port/src" > "$f/http-redirect.txt"
env -u no_proxy -u NO_PROXY -u http_proxy -u HTTP_PROXY -u https_proxy -u HTTPS_PROXY -u all_proxy -u ALL_PROXY curl -sv "http://127.0.0.1:$port/missing.txt" > "$f/http-verbose-404.txt" 2>&1
kill "$server"

# Real directory listings of the Go source tree.
(cd "$(go env GOROOT)" && find src -type f | sort) > "$f/find-listing.txt"
(cd "$(go env GOROOT)/src" && LC_ALL=C ls -lR crypto/tls net/http) > "$f/ls-recursive.txt" 2>&1
ls -l "$f"
