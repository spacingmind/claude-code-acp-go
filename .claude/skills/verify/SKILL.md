---
name: verify
description: Run this repo's standard build+test+lint sequence, plus a spec-traceability check against the current plan's Test Scenarios if one exists. Use this before considering any bounded code change done (AGENTS.md rule b), or whenever asked to "verify", "check the build", or "make sure it still works".
---

Run, in order, stopping at (but reporting) the first failure — this mirrors
`.github/workflows/ci.yml` exactly, so a green `verify` means CI will be
green too:

1. `go build -buildvcs=false ./...` — must produce no errors.
2. `go test -race ./...` — must pass.
3. `go vet ./...` — must pass.
4. `test -z "$(gofmt -l .)"` — no file may need gofmt formatting.
5. `golangci-lint run` — must pass (CI runs this as a separate `lint` job
   against `.golangci.yml`).

If a step fails, paste the raw failing output, then stop — do not run later
steps on top of a known failure unless asked to. Fix the underlying code
rather than the CI workflow, unless the workflow itself is the bug.

## Spec traceability (if a plan doc exists)

If the current work has a `docs/plans/active/<slug>.md` (see the `plan`
skill) with a Test Scenarios section, check each named scenario has an
actual corresponding test — not just that the test suite passes overall, but
that nothing in the list was quietly skipped. Report any scenario with no
matching test explicitly; don't silently pass over the gap. This step is
skipped (not failed) when no plan doc applies to the current work.

Report format: one line per step (`build: ok`, `test: ok`, `vet: ok`,
`gofmt: ok`, `lint: ok` or `FAILED`, `spec-traceability: 4/4 scenarios
covered` or listing the gap), followed by raw output for any failure.
