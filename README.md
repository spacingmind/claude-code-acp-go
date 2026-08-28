# claude-code-acp-go

A pure-Go [Agent Client Protocol](https://agentclientprotocol.com) (ACP)
agent for [Claude Code](https://claude.com/product/claude-code) — lets any
ACP-speaking editor (Zed, etc.) drive Claude Code as a single static Go
binary, no Node.js required.

Built on [`claude-agent-sdk-go`](https://github.com/spacingmind/claude-agent-sdk-go),
which already drives the `claude` CLI's own native protocol (persistent
client, full control-protocol handshake, hooks, session resume, in-process
MCP tools). This repo adds the ACP-facing side: a hand-rolled JSON-RPC
layer (no third-party ACP library dependency) translating between ACP's
wire protocol and that SDK.

Differs from Anthropic/Zed's own official bridge
(`@zed-industries/claude-code-acp`, an npm package) by being a native Go
binary with no Node.js dependency, built on a more feature-complete
underlying Claude Code driver.

## Install

```bash
go install github.com/spacingmind/claude-code-acp-go/cmd/claude-code-acp@latest
```

Requires the `claude` CLI to be installed and on `PATH`.

## Usage

`claude-code-acp` speaks ACP as newline-delimited JSON-RPC 2.0 over
stdin/stdout — point any ACP-compatible client at the binary. For Zed, add
it as a custom agent server in `settings.json`:

```json
{
  "agent_servers": {
    "Claude Code": {
      "command": "claude-code-acp"
    }
  }
}
```

## Features

- **Session lifecycle**: `session/new`, `session/load`, `session/resume`,
  `session/list`, `session/set_mode` — new sessions and resuming/replaying
  previously persisted ones.
- **Prompt turns**: `session/prompt` streams Claude's response back as
  ACP `session/update` notifications (text chunks, tool calls and their
  status updates, current-mode changes), with `session/cancel` support
  mid-turn.
- **Tool call translation**: Claude Code tool uses (Read, Edit, Write,
  Bash, Grep, Glob, WebFetch, ...) are mapped to ACP `ToolKind`s
  (`read`, `edit`, `execute`, `search`, `fetch`, `other`) with live status
  updates as they run.
- **Permissions**: tool permission requests are forwarded to the ACP
  client via `session/request_permission`, unblocking immediately if the
  turn is cancelled.

## Architecture

- `transport.go` — newline-delimited JSON-RPC 2.0 connection (single
  reader goroutine, mutex-guarded writer), mirroring the transport used
  in `claude-agent-sdk-go`.
- `types.go` — ACP wire types: initialize/capabilities, content blocks,
  session updates, tool calls, permission requests.
- `agent.go` — method dispatch and per-session/per-turn state.
- `translate.go` — Claude Code SDK messages → ACP `SessionUpdate`s.
- `permission.go` — bridges Claude Code's permission-policy interface to
  ACP's `session/request_permission`.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for build/test/lint commands and
the PR workflow, and `docs/plans/completed/` for the specs this was
implemented against.

## License

MIT
