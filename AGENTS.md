# AGENTS.md

Repository protocol entrypoint for agents working in this repo.

## Project overview

See [README.md](README.md).

## Workspace map

- Root Go package (`package acp`) — the hand-rolled ACP protocol layer
  (JSON-RPC framing, method dispatch, wire types) plus the Claude Code
  agent adapter built on top of it.
- `*_test.go` — tests.
- `docs/` — ADRs (`docs/decisions/`) and active/completed plans
  (`docs/plans/`).
- Depends on `github.com/spacingmind/claude-agent-sdk-go` for driving the
  `claude` CLI — this repo does NOT reimplement Claude Code's own wire
  protocol, only the ACP-facing side.

## Workflow rules

Same as `claude-agent-sdk-go`'s AGENTS.md:

**(a) Read-only questions.** Inspect the smallest relevant surface, answer
with evidence.

**(b) Bounded changes.** Smallest coherent change; run `go build`,
`go test -race`, `go vet`, `gofmt -l .` (and `golangci-lint run` once a
config exists) before considering a change done.

**(c) Multi-session work — spec-driven.** Before implementation starts,
create `docs/plans/active/<slug>.md` with Acceptance Criteria and Test
Scenarios, plus Decisions/Progress/Validation sections. Move to
`docs/plans/completed/` when every acceptance criterion is validated.

**(d) Material ambiguity.** If a choice materially affects public API
shape or ACP wire-protocol behavior and isn't already decided, stop and
ask rather than deciding unilaterally.

## Branching & release workflow

- `main` is release-only. Do not commit or push directly to `main`.
- `develop` is the integration branch. Feature/fix work happens on
  short-lived branches off `develop`, merged back via PR (squash-merge is
  fine here, and is the default).
- **Releasing means opening a PR from `develop` into `main`, merged with
  "Create a merge commit" -- NOT squash.** Squash-merging `develop` into
  `main` severs ancestry (the resulting commit on `main` has no shared
  history with `develop`'s tip), so the next `develop`->`main` merge
  computes a stale `merge-base` and produces spurious conflicts on any
  line both branches touched since (e.g. `go.mod`'s dependency version)
  even when the content isn't actually in conflict. A real merge commit
  keeps `main` a true descendant of `develop`, so this class of conflict
  can't recur. Merging that PR triggers everything downstream:
  1. `.github/workflows/release-please.yml` (on push to `main`) runs
     [release-please](https://github.com/googleapis/release-please),
     which reads Conventional Commits since the last release and either
     opens/updates a `chore(main): release X.Y.Z` PR (version bump +
     `CHANGELOG.md`), or — if that release PR is what just got merged —
     creates the `vX.Y.Z` git tag directly.
  2. Pushing that tag triggers `.github/workflows/release.yml`, which runs
     `goreleaser` to build the actual GitHub Release (changelog grouping,
     `pkg.go.dev` links). `release-please` also creates a plain release
     for the tag, but `goreleaser`'s `release.mode: replace` overwrites it
     with the nicer-formatted one — deliberately NOT using
     `skip-github-release`, since that flag breaks release-please's own
     tag-tracking (see `googleapis/release-please#1561`).
- **Commit messages merged into `main` must follow [Conventional
  Commits](https://www.conventionalcommits.org/)** (`feat:`, `fix:`,
  `feat!:`/`BREAKING CHANGE:` footer for breaking changes, `chore:`/
  `docs:`/`ci:`/`test:` for everything release-please should exclude from
  the changelog) — release-please cannot determine the correct version
  bump or changelog entry without this. This matters most for the PR
  title/squash-commit message that actually lands on `main`, not
  necessarily every commit on the feature branch.

## Key decisions already made

- **No third-party ACP library dependency** (explicitly chosen over
  `github.com/coder/acp-go-sdk` despite it being a viable, well-adopted
  option) — this repo hand-rolls its own minimal ACP JSON-RPC layer
  against the official schema at
  `github.com/agentclientprotocol/agent-client-protocol` (`schema/v1/schema.json`,
  ACP protocol version 1, the current stable version).
- Claude Code does its own filesystem I/O directly through the CLI
  subprocess — this adapter never calls `fs/read_text_file`/
  `fs/write_text_file`/`terminal/*` (no need to proxy file or terminal
  access through the ACP client).
