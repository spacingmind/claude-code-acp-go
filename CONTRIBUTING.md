# Contributing

Thanks for your interest in `claude-code-acp-go`. This is a small,
early-stage project (1-2 maintainers, no formal governance) — the process
below is intentionally lightweight.

## Building and testing locally

```sh
go build -buildvcs=false ./...
go test -race ./...
go vet ./...
gofmt -l .              # should print nothing
golangci-lint run
```

Run all of these (or the `verify` skill, if you're an agent working in
this repo) before opening a PR — this is the same sequence CI runs.

## Branching model

- **`main` is release-only.** It only receives merges via PR from
  `develop`. (The release automation on top of that — `release-please`
  and `goreleaser` — mirrors the sibling
  [`claude-agent-sdk-go`](https://github.com/spacingmind/claude-agent-sdk-go)
  project and is being wired up here too; check `.github/workflows/` for
  the current state if you need the exact mechanics.)
- **`develop` is the integration branch.** Base your feature/fix branch on
  `develop` and open your PR against `develop`.

## Conventional Commits

PRs are squash-merged, so **the PR title becomes the commit message that
lands in the repo's history** — and, once release automation is fully
wired up, the changelog entry generated when `develop` is released into
`main`. PR titles must follow
[Conventional Commits](https://www.conventionalcommits.org/):

- `feat: ...` — a new feature
- `fix: ...` — a bug fix
- `feat!: ...` or a `BREAKING CHANGE:` footer — a breaking change
- `chore: ...`, `docs: ...`, `ci: ...`, `test: ...` — maintenance work
  excluded from the changelog

Individual commits on your feature branch don't need to follow this strictly,
but the PR title does.

## Plan docs for nontrivial changes

This repo practices spec-driven development (see `AGENTS.md`, rule (c)).
For any change that spans more than a quick, obviously-bounded edit, write a
plan first: create `docs/plans/active/<slug>.md` with Acceptance Criteria
and Test Scenarios, plus Decisions/Progress/Validation sections, before
starting implementation, keep it updated as work proceeds, and move it to
`docs/plans/completed/` once every acceptance criterion is validated. Small,
self-contained fixes don't need one — use your judgment, and see
`AGENTS.md` for the full rule.

If a change materially affects the public API shape or ACP wire-protocol
behavior and isn't already decided (see `AGENTS.md`'s "Key decisions
already made"), please open an issue or discuss before investing in a
large PR — see `AGENTS.md` rule (d).

## Opening a PR

- Target `develop`, not `main`.
- Give the PR a Conventional Commits-style title.
- Fill out the PR template checklist.
