#!/usr/bin/env python3
# Copyright Abbreviatory.com 2026
# SPDX-License-Identifier: Apache-2.0
#
# The smoke check on a built binary, run by CI and by the release workflow on
# each system's binary before anything is published.
#
#   python3 scripts/smoke.py BIN_DIR
#
# BIN_DIR holds abbreviatory (abbreviatory.exe on Windows). The check: it
# reports the version in abbreviatory.go; every fixture in test/fixtures
# reduces within the budget, is detected as its format, and gives the same
# bytes twice; every reference in the output expands, and the whole-input
# reference gives the fixture back byte for byte; -json, -no-store, -keep and
# prune work; history (reduce -key, -full, history) and run (keeping the
# exit status) work; serve and mcp give the same report as reduce; and bad
# input fails cleanly.
import json
import urllib.error
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WINDOWS = os.name == "nt"
FIXTURES = ROOT / "test" / "fixtures"
FORMATS = {
    "gotest-json.txt": "gotest-json",
    "gotest-verbose.txt": "gotest",
    "gotest-short.txt": "gotest",
    "pytest-verbose.txt": "pytest",
    "pytest-short.txt": "pytest",
    "junit-pytest.xml": "junit",
    "app.log": "logs",
    "api-response.json": "json",
    "git-diff.txt": "diff",
    "http-json.txt": "http",
    "http-redirect.txt": "http",
    "http-verbose-404.txt": "http",
    "find-listing.txt": "listing",
    "ls-recursive.txt": "listing",
}
# References as the text shows them: "[ref ID:sel]" in reports, and in JSON
# outlines "ref": "ID:/pointer" or "(+N chars, ref ID:/pointer)".
REF = re.compile(r'(?:\[ref |"ref": "|, ref )([0-9a-f]{12})(?::([^\]"\s)]+))?')


class Failure(Exception):
    pass


def require(ok, message):
    if not ok:
        raise Failure(message)


def run(args, stdin=None, check=True):
    r = subprocess.run([str(a) for a in args], input=stdin, capture_output=True, timeout=300)
    if check:
        require(r.returncode == 0, f"{' '.join(str(a) for a in args[1:])} exited {r.returncode}:\n{r.stderr.decode(errors='replace')[-3000:]}")
    return r


def source_version():
    found = re.findall(r'^const Version = "(.*)"$', (ROOT / "abbreviatory.go").read_text(encoding="utf-8"), re.M)
    require(len(found) == 1, "abbreviatory.go has no single Version constant")
    return found[0]


def main():
    if len(sys.argv) != 2:
        print("usage: smoke.py BIN_DIR", file=sys.stderr)
        sys.exit(2)
    exe = Path(sys.argv[1]) / ("abbreviatory.exe" if WINDOWS else "abbreviatory")
    require(exe.is_file(), f"{exe} is missing")
    store = tempfile.mkdtemp(prefix="abbr-smoke-")
    checks = 0

    out = run([exe, "version"]).stdout.decode().strip()
    require(out == "abbreviatory " + source_version(), f"version says {out!r}, abbreviatory.go says {source_version()}")
    checks += 1

    for name, fmt in sorted(FORMATS.items()):
        path = FIXTURES / name
        data = path.read_bytes()
        for budget in (1024, 4096):
            r = run([exe, "reduce", "-store", store, "-budget", budget, "-json", path])
            res = json.loads(r.stdout)
            text = res["text"]
            require(res["format"] == fmt, f"{name}: detected as {res['format']}, want {fmt}")
            require(len(text.encode()) <= budget, f"{name}: {len(text.encode())} bytes over the budget of {budget}")
            require(res["input_bytes"] == len(data), f"{name}: input_bytes {res['input_bytes']}, file has {len(data)}")
            again = json.loads(run([exe, "reduce", "-store", store, "-budget", budget, "-json", path]).stdout)
            require(again["text"] == text, f"{name}: two runs differ")
            # Stdin and a file give the same result.
            piped = run([exe, "reduce", "-store", store, "-budget", budget], stdin=data).stdout.decode()
            require(piped.replace("\r\n", "\n") == text, f"{name}: stdin and file differ")
            refs = REF.findall(text)
            require(refs, f"{name}: no references in the output")
            for rid, sel in refs:
                require(rid == res["ref"], f"{name}: reference {rid}, result ref {res['ref']}")
                got = run([exe, "expand", "-store", store, f"{rid}:{sel}" if sel else rid]).stdout
                require(len(got) > 0, f"{name}: {rid}:{sel} expands to nothing")
            whole = run([exe, "expand", "-store", store, res["ref"]]).stdout
            require(whole == data, f"{name}: the whole-input reference doesn't give the file back")
            checks += 1

    # -no-store: no references, nothing stored.
    empty = tempfile.mkdtemp(prefix="abbr-smoke-empty-")
    out = run([exe, "reduce", "-store", empty, "-no-store", FIXTURES / "app.log"]).stdout.decode()
    require("[ref" not in out, "-no-store output has references")
    require(not any(Path(empty).rglob("*.gz")), "-no-store stored something")
    checks += 1

    # -keep surfaces a line that is otherwise noise.
    out = run([exe, "reduce", "-store", store, "-keep", "queue depth=3[0-9]", FIXTURES / "app.log"]).stdout.decode()
    require("MATCHED --keep" in out, "-keep match not shown")
    checks += 1

    # Bad input fails with a message, not a crash.
    r = run([exe, "reduce", "-store", store], stdin=bytes(range(256)) * 16, check=False)
    require(r.returncode == 1 and b"binary" in r.stderr, f"binary input: exit {r.returncode}, {r.stderr!r}")
    r = run([exe, "reduce", "-store", store, "-format", "json"], stdin=b'{"a": [1,', check=False)
    require(r.returncode == 1 and b"JSON" in r.stderr, f"broken JSON: exit {r.returncode}, {r.stderr!r}")
    r = run([exe, "reduce", "-store", store, "-budget", "100"], stdin=b"ERROR x\n", check=False)
    require(r.returncode == 2 and b"at least 512" in r.stderr, f"small budget: exit {r.returncode}, {r.stderr!r}")
    checks += 1

    # History: the second identical run is one line, -full shows the report,
    # and a changed run lists what changed.
    path = FIXTURES / "pytest-verbose.txt"
    out = run([exe, "reduce", "-store", store, "-key", "smoke", path]).stdout.decode()
    require("run 1; no earlier runs to compare with" in out, f"first tracked run:\n{out}")
    out = run([exe, "reduce", "-store", store, "-key", "smoke", path]).stdout.decode()
    require(out.count("\n") == 1 and "identical to run 1" in out, f"identical run:\n{out}")
    out = run([exe, "reduce", "-store", store, "-key", "smoke", "-full", path]).stdout.decode()
    require("since run 2" in out and "FAILED" in out, f"-full run:\n{out}")
    lines = path.read_bytes().splitlines(keepends=True)
    fewer = b"".join(l for l in lines if b"test_parse_" not in l)
    out = run([exe, "reduce", "-store", store, "-key", "smoke"], stdin=fewer).stdout.decode()
    require("since run 3:" in out and ("fixed:" in out or "not run:" in out), f"changed run:\n{out}")
    hist = json.loads(run([exe, "history", "-store", store, "-json", "smoke"]).stdout)
    require([r["run"] for r in hist["runs"]] == [1, 2, 3, 4], f"history runs: {hist}")
    checks += 1

    # run keeps the command's exit status.
    r = run([exe, "run", "-store", store, "-key", "smoke-run", "--", exe, "version"])
    require(b"run 1, exit status 0" in r.stdout and b"abbreviatory " in r.stdout, f"run: {r.stdout!r}")
    r = run([exe, "run", "-store", store, "-key", "smoke-run2", "--", exe, "no-such-command"], check=False)
    require(r.returncode == 2 and b"exit status 2" in r.stdout, f"run of a failing command: exit {r.returncode}, {r.stdout!r}")
    checks += 1

    # serve: the HTTP service gives the same report as the command line.
    path = FIXTURES / "gotest-json.txt"
    want = run([exe, "reduce", "-store", store, path]).stdout.decode().replace("\r\n", "\n")
    proc = subprocess.Popen([str(exe), "serve", "-addr", "127.0.0.1:0", "-store", store, "-token", "smoke"],
                            stderr=subprocess.PIPE, text=True)
    try:
        line = proc.stderr.readline()
        found = re.search(r"http://\S+", line)
        require(found, f"serve printed {line!r}")
        base = found.group(0)
        import urllib.request
        def call(method, url, body=None, accept=None):
            req = urllib.request.Request(base + url, data=body, method=method, headers={"Authorization": "Bearer smoke"})
            if accept:
                req.add_header("Accept", accept)
            with urllib.request.urlopen(req, timeout=60) as resp:
                return resp.read()
        health = json.loads(call("GET", "/healthz"))
        require(health["version"] == source_version(), f"healthz: {health}")
        got = call("POST", "/reduce", path.read_bytes(), "text/plain").decode()
        require(got == want, "serve and reduce give different reports")
        res = json.loads(call("POST", "/reduce?budget=1024", path.read_bytes()))
        whole = call("GET", "/expand/" + res["ref"])
        require(whole == path.read_bytes(), "serve's whole-input reference doesn't give the file back")
        try:
            urllib.request.urlopen(base + "/reduce", data=b"x", timeout=60)
            require(False, "serve answered without the token")
        except urllib.error.HTTPError as e:
            require(e.code == 401, f"without the token: {e.code}")
    finally:
        proc.terminate()
        proc.wait(timeout=30)
    checks += 1

    # mcp: an agent's session over standard input and output.
    msgs = [
        {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "smoke", "version": "1"}}},
        {"jsonrpc": "2.0", "method": "notifications/initialized"},
        {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
        {"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "reduce", "arguments": {"path": str(path)}}},
    ]
    r = run([exe, "mcp", "-store", store], stdin="".join(json.dumps(m) + "\n" for m in msgs).encode())
    answers = {m["id"]: m for m in (json.loads(l) for l in r.stdout.decode().splitlines() if l.strip())}
    require(answers[1]["result"]["serverInfo"]["version"] == source_version(), f"mcp initialize: {answers.get(1)}")
    require([t["name"] for t in answers[2]["result"]["tools"]] == ["reduce", "run", "expand", "history"], f"mcp tools: {answers.get(2)}")
    require(answers[3]["result"]["content"][0]["text"].replace("\r\n", "\n") == want, "mcp and reduce give different reports")
    checks += 1

    out = run([exe, "prune", "-store", store, "-max-size", "0"]).stdout.decode()
    require(out.startswith("removed "), f"prune said {out!r}")
    r = run([exe, "expand", "-store", store, res["ref"]], check=False)
    require(r.returncode == 1, "an original survived prune -max-size 0")
    checks += 1

    print(f"smoke check passed: {checks} checks on {exe.name} {source_version()}")


if __name__ == "__main__":
    try:
        main()
    except Failure as e:
        print(f"smoke check FAILED: {e}", file=sys.stderr)
        sys.exit(1)
