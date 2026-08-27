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

## Status

Early development — see `docs/plans/active/` for the current spec.

## License

MIT
