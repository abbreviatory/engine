#!/bin/sh
# Copyright Abbreviatory.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# Builds the browser demo into a site checkout: the engine compiled to
# WebAssembly, Go's loader for it, the page (demo/index.html) and samples
# from the test fixtures. The site serves it at /demo/.
#
#   sh scripts/build-demo.sh SITE_DIR
#
# SITE_DIR is a checkout of the abbreviatory.com site; the demo goes to
# SITE_DIR/static/demo/. Needs Go and Python 3.
set -eu
site=${1:?usage: build-demo.sh SITE_DIR}
cd "$(dirname "$0")/.."
out="$site/static/demo"
rm -rf "$out"
mkdir -p "$out/samples"

GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o "$out/abbreviatory.wasm" ./cmd/abbreviatory-wasm
goroot=$(go env GOROOT)
if test -f "$goroot/lib/wasm/wasm_exec.js"; then
	cp "$goroot/lib/wasm/wasm_exec.js" "$out/"
else
	cp "$goroot/misc/wasm/wasm_exec.js" "$out/"
fi
cp "$goroot/LICENSE" "$out/wasm_exec.LICENSE.txt"
cp demo/index.html "$out/index.html"

f=test/fixtures
cp "$f/gotest-verbose.txt" "$f/pytest-verbose.txt" "$f/junit-pytest.xml" "$f/api-response.json" "$out/samples/"
head -n 12000 "$f/app.log" > "$out/samples/app.log"

# The earlier of the two runs for "Compare two runs": TestFieldSpaces still
# passed, TestFieldNegative still failed.
python3 - "$f/gotest-verbose.txt" "$out/samples/gotest-run1.txt" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
text = open(src, encoding="utf-8").read()
spaces = ('=== RUN   TestFieldSpaces\n'
          '    parse_test.go:33: Field("width = 12"): unexpected error: strconv.Atoi: parsing " 12": invalid syntax\n'
          '    parse_test.go:36: Field("width = 12") = 0, want 12\n'
          '--- FAIL: TestFieldSpaces (0.00s)\n')
negative = '=== RUN   TestFieldNegative\n--- PASS: TestFieldNegative (0.00s)\n'
assert spaces in text and negative in text, "the fixture changed; update build-demo.sh"
text = text.replace(spaces, '=== RUN   TestFieldSpaces\n--- PASS: TestFieldSpaces (0.00s)\n')
text = text.replace(negative, '=== RUN   TestFieldNegative\n'
                    '    parse_test.go:28: Field("depth=-3") = 3, want -3\n'
                    '--- FAIL: TestFieldNegative (0.00s)\n')
open(dst, "w", encoding="utf-8").write(text)
PY
ls -la "$out" "$out/samples" | awk 'NF > 5 {print $5, $NF}'
