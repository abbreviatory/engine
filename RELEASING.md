# Releasing Abbreviatory

Releases go out on their own. Nobody clicks Publish. This file is the record of
how that works, for whoever (person or Claude session) works on this repo next.

## The short version

1. Raise `const Version = "X.Y.Z"` in `abbreviatory.go`.
2. Write what changed into the commit message.
3. Push the commit to `main`.

When the `test` workflow passes on that commit (the full suite and the smoke
check on Linux and macOS, and the smoke check on a Windows binary on a Windows
runner), the `release` workflow builds the binaries, smoke-tests them, then
tags the commit `vX.Y.Z` and publishes the release "Abbreviatory vX.Y.Z" with
the archives and SHA256SUMS attached. If any build or check fails, nothing is
tagged and nothing goes public. Fix it, push again, and the next green run
picks it up.

Pushing without raising the version is fine: the release workflow sees the tag
already exists and stops quietly.

## What it does, step by step

[.github/workflows/release.yml](.github/workflows/release.yml) starts when a
`test` run finishes; the push alone doesn't start it.

- **plan.** Goes on only if the `test` run passed, came from a push to `main` in
  this repo, and `main` still points at that commit (if `main` moved on, the run
  for the newer commit decides). Reads `Version` from `abbreviatory.go`. Stops
  quietly if `vX.Y.Z` is already a tag. Fails loudly if the version is lower than
  the newest tag. [scripts/release-plan.py](scripts/release-plan.py) has the
  rules, and [scripts/release_plan_test.py](scripts/release_plan_test.py) its cases.
- **linux.** Builds `abbreviatory` from that exact commit, pure Go with no
  cgo so it is static, on x86-64 and ARM runners, checks it is static, and runs
  [scripts/smoke.py](scripts/smoke.py) on it.
- **macos.** Builds for Apple silicon and Intel. The Apple silicon binary runs
  the smoke check; the Intel one is checked for its architecture.
- **windows.** Builds the `.exe` on Linux.
- **windows-check.** Runs the smoke check on the Windows binary on a Windows
  runner.
- **release.** Runs only after every build and check passed. Writes SHA256SUMS and
  the notes, then creates the tag and the release in one step with
  `gh release create --target <commit>`, on the repo's own `GITHUB_TOKEN`
  (`contents: write` on this job only). A `concurrency` group per tag means two
  runs for the same version can't publish twice.

The smoke check, on each binary: it reports the version in `abbreviatory.go`;
every fixture in `test/fixtures` reduces within a 1,024 and a 4,096 byte
budget, is detected as its format, gives the same bytes twice and the same
from a file as from standard input; every reference in the output expands, and
the whole-input reference gives the fixture back byte for byte; `-json`,
`-no-store`, `-keep` and `prune` work; and binary input, broken JSON and a
budget too small fail with a message; a history gives "run 1", then one line
for an identical run, the whole report with `-full`, and the changes for a
changed run; and `run` keeps the command's exit status.

## Files that go out

| System | File |
|---|---|
| Linux, x86-64 (static) | abbreviatory_linux_amd64.tar.gz |
| Linux, ARM64 (static) | abbreviatory_linux_arm64.tar.gz |
| macOS, Apple silicon | abbreviatory_darwin_arm64.tar.gz |
| macOS, Intel | abbreviatory_darwin_amd64.tar.gz |
| Windows, x86-64 | abbreviatory_windows_amd64.zip |
| Checksums | SHA256SUMS |

Each archive holds the binary, LICENSE, NOTICE, THIRD_PARTY_LICENSES.md and
README.md. Archive names carry no version on purpose: links like
`https://github.com/abbreviatory/engine/releases/latest/download/abbreviatory_linux_amd64.tar.gz`
keep working from one release to the next, so the README and the site never need
editing for a release.

## After a release

A Claude session can't create tags or releases itself, which is why the workflow
does it on the repository's token. After pushing a raised version, check:

1. The `test` run and then the `release` run finished green
   (`gh run list --repo abbreviatory/engine`).
2. The release exists with all six files
   (`gh release view vX.Y.Z --repo abbreviatory/engine`).
3. The `latest/download` links resolve, and a downloaded archive's SHA256 matches
   SHA256SUMS.
4. A downloaded Linux binary runs: `abbreviatory version` names the release.

## By hand

- A release published by hand gets the binaries attached by the same workflow.
- To rebuild the files of an existing tag, run the `release` workflow by hand
  with the tag (`gh workflow run release --repo abbreviatory/engine -f tag=vX.Y.Z`).
