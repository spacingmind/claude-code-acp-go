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
