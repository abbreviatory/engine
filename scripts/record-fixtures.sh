#!/bin/sh
# Copyright Abbreviatory.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Records the test-runner fixtures from the sample projects in
# test/fixtures/src: real go test output (JSON, verbose, plain), real pytest
# output (verbose, plain) and pytest's JUnit XML report. The samples fail on
# purpose. Then writes the generated log and JSON fixtures.
#
#   sh scripts/record-fixtures.sh
#
# Needs Go, Python 3 and pytest. Timings and addresses in the output change
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
ls -l "$f"
