# ACP Agent Core (phase 1)

A pure-Go, single-binary ACP (Agent Client Protocol) agent for Claude Code,
wrapping `github.com/spacingmind/claude-agent-sdk-go` (which already drives
the `claude` CLI's own native protocol). Lets any ACP-speaking editor/client
(Zed, etc.) talk to Claude Code without a Node.js dependency — differs from
Anthropic/Zed's own official bridge (`@zed-industries/claude-code-acp`, an
npm package) by being a native Go binary built on our own, more complete
Claude Code driver (full control protocol, hooks, session resume, SDK-MCP
bridge already implemented there).

## Research basis

- Official ACP schema: `github.com/agentclientprotocol/agent-client-protocol`,
  `schema/v1/schema.json` (170 `$defs`, protocol version 1, the current
  stable version) — read in full via a research pass 2026-08-28.
- Community precedent reviewed: `beyond5959/acp-adapter` (hand-rolled ACP
  server, ~5500 lines) and `coder/acp-go-sdk` (228★, Coder-backed, 134
  known dependents, schema-generated) — the latter was considered as a
  dependency and explicitly rejected in favor of hand-rolling, per the
  user's decision 2026-08-28 (see this repo's `AGENTS.md`).
- Confirmed via `coder/acp-go-sdk`'s own `connection.go`: ACP over stdio
  is JSON-RPC 2.0 framed as **line-delimited JSON** (NDJSON) — same
  framing style `claude-agent-sdk-go`'s `transport.go` already uses for
  Claude Code's native protocol, so that file's patterns (scanner-based
  line reads, mutex-guarded line writes) can be mirrored here.

## Scope

This phase: `initialize`, `session/new`, `session/prompt`, `session/cancel`,
`session/update` notifications (message chunks + tool calls), and
`session/request_permission`. This is the minimum for "an ACP client can
open a session, send a prompt, watch it stream, and approve/deny tool
calls" — a fully usable agent, not a stub.

Explicitly deferred to a later phase (cheap later since
`claude-agent-sdk-go` already has the building blocks): `session/load`,
`session/resume`, `session/list`, `session/set_mode`. Explicitly out of
scope entirely (not needed — Claude Code does its own filesystem/terminal
I/O): `fs/read_text_file`, `fs/write_text_file`, all `terminal/*`,
elicitation, `session/set_config_option`, `authenticate`/`logout` (Claude
Code's own CLI handles login out-of-band).

## Acceptance Criteria

### A. JSON-RPC transport & dispatch

1. A `Connection` type (or similar) reads NDJSON lines from an `io.Reader`
   (stdin in production) and writes NDJSON lines to an `io.Writer` (stdout
   in production), each line one of: a request
   (`{"jsonrpc":"2.0","id":<RequestID>,"method":"<string>","params":{...}}`),
   a response (`{"jsonrpc":"2.0","id":<RequestID>,"result":{...}}` or
   `{"jsonrpc":"2.0","id":<RequestID>,"error":{"code":int,"message":string,"data"?:any}}`),
   or a notification (`{"jsonrpc":"2.0","method":"<string>","params":{...}}`,
   no `id`). `RequestID` is `null | int64 | string` per the ACP schema —
   model as a small wrapper type or `json.RawMessage`, whichever is
   simpler to round-trip correctly (must echo the exact `id` value back
   on a response, including string IDs, not just assume integers).
2. Exactly one goroutine reads the NDJSON stream (mirrors
   `claude-agent-sdk-go`'s single-reader-goroutine pattern); dispatch by
   presence of `id`+`method` (request), `method` only (notification), or
   `id`+(`result`|`error`) (response, correlated to a pending outbound
   request map).
3. This process is itself the ACP **Agent** — the ACP **Client** (the
   editor) sends us requests (`initialize`, `session/new`,
   `session/prompt`) and notifications (`session/cancel`); we send it
   requests (`session/request_permission`) and notifications
   (`session/update`). Both directions run over the same single stdio
   connection — outbound request writes must be safe for concurrent
   callers (multiple in-flight `session/request_permission` calls across
   concurrent tool calls), matching the mutex-guarded-write pattern
   `claude-agent-sdk-go`'s transport already uses.
4. An unrecognized inbound method returns a JSON-RPC error response with
   code `-32601` ("Method not found"); malformed JSON on a line is not
   fatal to the connection (log/skip, matching `claude-agent-sdk-go`'s
   "don't fail the whole session over one bad line" philosophy) unless it
   makes the line fundamentally unparseable as any JSON-RPC envelope at
   all, in which case it's dropped silently.

### B. `initialize`

5. Handles `{"method":"initialize","params":{"protocolVersion":1,...}}`.
   Responds with `InitializeResponse{ProtocolVersion: 1, AgentCapabilities{...}, AuthMethods: []}`.
   `AgentCapabilities` for this phase: `LoadSession: false` (phase 2),
   `PromptCapabilities{Image:false,Audio:false,EmbeddedContext:false}`
   (text-only prompt content for this phase), `McpCapabilities{Http:false,Sse:false}`
   (stdio MCP servers only, since that's the only variant agents MUST
   support and the only one `claude-agent-sdk-go`'s `WithMCPConfig`
   currently needs), `SessionCapabilities{}` (no list/delete/resume/close
   yet). `AuthMethods` always `[]` — `authenticate`/`logout` are never
   implemented.

### C. `session/new`

6. Handles `{"method":"session/new","params":{"cwd":"...","mcpServers":[...]}}`.
   `cwd` (required, absolute path) becomes the `worktreePath` passed to
   `claude-agent-sdk-go`'s `New()`. `mcpServers` (required array, may be
   empty) — for this phase, only the no-`type`/`"stdio"` variant is
   supported (`McpServerStdio{Name,Command,Args,Env}`); reject `"http"`/`"sse"`
   entries with an `invalid_params` error (code `-32602`) since
   `McpCapabilities` didn't advertise them. Stdio servers translate into
   a `--mcp-config` JSON blob via the existing SDK's `WithMCPConfig`
   option (build the `{"mcpServers":{...}}` object from the ACP request's
   server list).
7. Generates a session ID (a UUID is fine — ACP just requires the string
   to be unique and opaque), constructs the underlying
   `claude-agent-sdk-go` `Client` via `New(cwd, opts...)`, and tracks the
   `(sessionID → *Client)` mapping for the connection's lifetime. Returns
   `NewSessionResponse{SessionID: <generated>}` (no `Modes`/`ConfigOptions`
   in this phase).
8. If `New()` fails (e.g. `CLINotFoundError`, `CLIConnectionError` from
   the underlying SDK — see `claude-agent-sdk-go`'s error hierarchy),
   translate to a JSON-RPC error response with a reasonable code (internal
   error `-32603` is fine, this isn't one of ACP's specific error codes)
   and a message derived from the underlying error's `Error()` string.

### D. `session/prompt`

9. Handles `{"method":"session/prompt","params":{"sessionId":"...","prompt":[...]}}`.
   For this phase, only `ContentBlock` variant `"text"` (`TextContent{Text}`)
   is supported in the `prompt` array (matches `PromptCapabilities` not
   advertising image/audio/embeddedContext) — concatenate multiple text
   blocks with newlines if more than one is present, or reject non-text
   blocks with `invalid_params` if any appear (the client shouldn't send
   them given our advertised capabilities, but don't trust that blindly).
10. Looks up the session's `Client` by `sessionId` (unknown ID → JSON-RPC
    error, ACP's `Resource not found` code `-32002`), calls
    `Client.Query`(or `QueryWithSession`) with the extracted text, then
    consumes `Client.ReceiveResponse` — translating each streamed message
    into `session/update` notifications (see section F) — until the
    terminal `ResultMessage` arrives. Responds to the original
    `session/prompt` request with `PromptResponse{StopReason: <mapped>}`
    only after that terminal message (this is a blocking RPC from the
    client's perspective; streaming happens entirely via the interleaved
    `session/update` notifications sent before the response).
11. `StopReason` mapping from the underlying SDK's `ResultMessage`: a
    clean result with no error → `"end_turn"`; a result whose
    `StopReason`/`TerminalReason` fields (or the fact that a
    `session/cancel` was received for this turn — see AC 13) indicate the
    turn was interrupted → `"cancelled"`; anything else error-shaped →
    pick the closest ACP `StopReason` (`"refusal"` for a permission-denial-
    driven stop if detectable, `"max_turn_requests"` if the underlying
    result indicates a turn-count limit, else fall back to `"end_turn"`
    with the error surfaced via a preceding `agent_message_chunk` — exact
    heuristic is an implementer judgment call, document whichever mapping
    is chosen).

### E. `session/cancel`

12. Handles the notification `{"method":"session/cancel","params":{"sessionId":"..."}}`
    (no response — it's a notification). Looks up the session's `Client`
    and calls `Client.Interrupt(ctx)`. Per the ACP spec: the in-flight
    `session/prompt` response for that session MUST eventually be sent
    with `StopReason: "cancelled"`, and any pending
    `session/request_permission` request for that session MUST be
    answered with `RequestPermissionOutcome{Cancelled: true}` rather than
    left hanging.
13. Track "this session's current turn was cancelled" state (a simple
    per-session flag, cleared when a new `session/prompt` starts) so AC
    11's `StopReason` mapping and AC 12's permission-request handling can
    both consult it.
14. The generic protocol-level `$/cancel_request` notification (aborting
    one specific in-flight JSON-RPC call by `RequestId`, unrelated to
    `session/cancel`) may be a no-op for this phase (the spec allows
    implementations to ignore it if they can't act on it) — don't conflate
    it with `session/cancel`.

### F. `session/update` — translating SDK messages to ACP

15. While consuming `ReceiveResponse` in AC 10, translate:
    - `AssistantMessage` containing `TextBlock`s → one `session/update`
      per text block (or coalesced — implementer's call) with
      `SessionUpdate{AgentMessageChunk: &ContentChunk{Content: TextContent{Text}}}`.
    - `AssistantMessage` containing a `ToolUseBlock` → one `session/update`
      with `SessionUpdate{ToolCall: &ToolCall{ToolCallID: <block.ID>, Title: <derived>, Kind: <mapped>, Status: "pending"}}`
      (see AC 16 for `Title`/`Kind` derivation), followed once the tool
      actually starts running (if that's observably distinct from
      "requested" in the message stream — check what
      `claude-agent-sdk-go` actually surfaces here; if there's no
      separate "started" signal, going straight to `"in_progress"` on the
      same update is fine) by a `ToolCallUpdate` to `"in_progress"`.
    - `UserMessage`/`ToolResultBlock` (tool results echoed back) → a
      `session/update` with `SessionUpdate{ToolCallUpdate: &ToolCallUpdate{ToolCallID: <matching ID>, Status: "completed"|"failed" (from IsError), Content: [{Content: TextContent{...}}]}}`.
    - `ThinkingBlock` → `SessionUpdate{AgentThoughtChunk: &ContentChunk{...}}`.
    - Everything else the SDK's `Message`/`ContentBlock` types can carry
      (system messages, hook events, task-lifecycle messages, stream
      events, rate-limit events) — no ACP equivalent in this phase's
      scope, drop silently (don't error on unrecognized SDK message
      types; ACP has no slot for them yet).
16. `ToolKind` mapping (best-effort, from the tool's name): `Read`→`"read"`,
    `Edit`/`MultiEdit`/`Write`/`NotebookEdit`→`"edit"`, `Bash`/`BashOutput`→`"execute"`,
    `Grep`/`Glob`→`"search"`, `WebFetch`/`WebSearch`→`"fetch"`, anything
    unrecognized (including MCP tool names, `mcp__server__tool`) →
    `"other"`. `Title` derivation: a short human-readable string from the
    tool name + a summarized input (e.g. `"Read foo.go"`, `"Bash: go test ./..."`)
    — exact format is an implementer's call, doesn't need to be
    exhaustive, just reasonable.

### G. `session/request_permission`

17. A `PermissionPolicy` implementation (satisfying
    `claude-agent-sdk-go`'s existing `PermissionPolicy` interface) that,
    instead of deciding locally, sends an outbound
    `session/request_permission` request over the ACP connection and
    blocks on the response. Request shape:
    `RequestPermissionRequest{SessionID, ToolCall: <a ToolCallUpdate describing the pending call — reuse the same ToolCallID/Title/Kind already generated for this tool use in AC 15>, Options: [...]}`.
    `Options` for this phase: a fixed, simple set —
    `{OptionID:"allow", Name:"Allow", Kind:"allow_once"}` and
    `{OptionID:"deny", Name:"Deny", Kind:"reject_once"}` (don't attempt
    "always allow"/"always reject" persistence in this phase — that would
    need session-permission-rule tracking the underlying SDK doesn't
    expose a clean hook for yet; keep it binary for now).
18. Response handling: `RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID:"allow"}}` →
    `PermissionPolicy.Decide` returns `allow=true`; `OptionID:"deny"` →
    `allow=false`; `RequestPermissionOutcome{Cancelled: true}` (or the
    underlying request timing/erroring out because the session was
    cancelled per AC 12) → also `allow=false` with a denial message
    indicating cancellation.
19. Concurrent tool calls awaiting permission simultaneously must each
    get their own correctly-correlated `session/request_permission`
    round trip (falls out of AC 1-3's request-ID correlation, no
    additional synchronization needed here beyond what the transport
    layer already provides).

## Test Scenarios

**Transport**
- A scripted fake ACP client (a test harness analogous to
  `claude-agent-sdk-go`'s `fakecli_test.go`, but speaking ACP JSON-RPC
  instead of Claude Code's native protocol) sends a request, receives the
  correctly-`id`-correlated response.
- Concurrent outbound requests (two simultaneous
  `session/request_permission` calls) each get the right response
  correlated back, even if the fake client answers them out of order.
- An unrecognized inbound method gets a `-32601` error response.
- A malformed JSON line doesn't kill the connection; subsequent
  well-formed traffic still works.

**Session lifecycle**
- `initialize` → `session/new` (stdio MCP server + empty) → `session/prompt`
  ("hello") → observe `session/update` notifications for the streamed
  text → `PromptResponse{StopReason:"end_turn"}`.
- `session/new` with an `"http"`-type MCP server (not supported this
  phase) → `invalid_params` error.
- Unknown `sessionId` on `session/prompt` → `-32002` resource-not-found
  error.

**Cancellation**
- `session/cancel` mid-turn → the in-flight `session/prompt` response
  arrives with `StopReason:"cancelled"`; a concurrently-pending
  `session/request_permission` for that session is answered
  `Outcome:{Cancelled:true}` rather than left hanging.

**Tool call translation**
- A turn involving one `Bash` tool call → `session/update` sequence:
  `tool_call` (`kind:"execute"`, `status:"pending"`) → `tool_call_update`
  (`status:"in_progress"`) → `tool_call_update` (`status:"completed"`,
  `content` carrying the tool's text output) — asserted in order.
- A failed tool result (`IsError:true`) → `tool_call_update` with
  `status:"failed"`.
- An MCP tool name (`mcp__myserver__mytool`) maps to `ToolKind:"other"`.

**Permission flow**
- `can_use_tool`-equivalent flow: a tool call triggers
  `session/request_permission`; the fake ACP client responds
  `Selected{OptionId:"allow"}` → the tool proceeds (assert via the
  downstream `tool_call_update` sequence); `OptionId:"deny"` → the tool
  does not run, a denial is surfaced.

## Decisions

- **No third-party ACP dependency** — hand-rolled against the official
  JSON Schema, per the user's explicit choice (2026-08-28), overriding
  the initially-recommended `coder/acp-go-sdk` despite its reasonable
  adoption signals (134 dependents, Coder-backed, semver releases).
- **Text-only prompt content, stdio-only MCP servers, no auth** for this
  phase — matches what `claude-agent-sdk-go` and Claude Code's own CLI
  actually need/support today; image/audio content blocks and http/sse
  MCP servers can be added later without restructuring anything here
  (just widening `AgentCapabilities` and the `ContentBlock`/`McpServer`
  unmarshal switch).
- **Session state kept in-process, in a map** — no persistence across
  process restarts in this phase (matches "phase 1 is the MVP," session
  resume/list is phase 2 and will reuse `claude-agent-sdk-go`'s existing
  local-disk session functions rather than inventing new state).
- **Binary allow/deny only for `session/request_permission`** — no
  "always allow" rule persistence in this phase; if that turns out to
  matter in practice, it's an additive follow-on (would need to track
  accepted rules per session and consult them before even asking, which
  `claude-agent-sdk-go`'s `PermissionPolicy` interface already supports
  structurally via `CanUseToolRequest.PermissionSuggestions`).

## Progress

Complete. Implemented across the root `package acp`:

- `types.go` — `RequestID` (comparable value type: null/int64/number-text/string,
  exact wire round-trip), JSON-RPC envelopes, all ACP wire types for AC 1-19.
  Tagged unions (`McpServer`, `ContentBlock`, `SessionUpdate`, `ToolCallContent`,
  `RequestPermissionOutcome`) are pointer-field wrapper structs with custom
  marshal/unmarshal; `McpServer.MarshalJSON` forces the `type` discriminator
  from the Go-side variant (a Go-constructed `McpServerHTTP` without `Type`
  set still marshals as `"type":"http"`, so test/wire round-trips agree).
- `transport.go` — `Connection`: single reader goroutine over NDJSON,
  mutex-guarded writes (concurrent outbound requests safe), pending-map
  keyed by `RequestID`, inbound request handlers run per-request goroutines,
  notifications dispatched synchronously in the reader goroutine (preserves
  `session/update` wire ordering for clients), unknown method → `-32601`,
  malformed line skipped without killing the connection.
- `agent.go` — `Agent`: `initialize` / `session/new` / `session/prompt` /
  `session/cancel` handlers; per-session turn context (canceled by
  `session/cancel`) + cancelled flag; sessions closed on connection end
  (`Agent.closeAll`, awaited via `Agent.Closed()`).
- `translate.go` — SDK message → `session/update` translation with
  mutex-guarded per-session tool-call title/kind state (written from the
  message-draining goroutine, read from the SDK's `can_use_tool` handler
  goroutine). ToolKind mapping and title derivation per AC 16.
- `permission.go` — `acpPermissionPolicy`: sends `session/request_permission`
  (binary allow/deny options), round trip scoped to both the caller's and the
  session's turn context so `session/cancel` resolves it (AC 12/18).
- `cmd/claude-code-acp/main.go` — thin binary entrypoint over `acp.Run`.
- Tests: two-layer harness (fake ACP client over `io.Pipe` ↔ Agent ↔ fake
  `claude` CLI via test-binary re-exec, `WithCLIPath` + `WithEnv`), covering
  every Test Scenario below.

Go-side deviations from the plan doc (wire shape unchanged):

- `RequestID` is a comparable struct (kind + fields), not `json.RawMessage` —
  needed to key the pending-response map by value.
- SDK messages arrive as value types (`AssistantMessage`, not
  `*AssistantMessage`); translation type-switches on values.
- `tool_call_update` carries optional `title`/`kind` fields (in the ACP
  schema) so permission requests reuse AC 15 metadata without extra state.
- StopReason mapping (AC 11 heuristic, documented): cancelled flag →
  `cancelled`; `PermissionDenials` non-empty or `StopReason=="refusal"` →
  `refusal`; `TerminalReason`/`StopReason` indicates turn limit →
  `max_turn_requests`; else `end_turn`.

## Validation

All of `go build -buildvcs=false ./...`, `go vet ./...`, `gofmt -l .`,
`golangci-lint run` (0 issues), and `go test -race -count=1 ./...` clean;
zero leaked fake-CLI processes after runs (verified via `ps`). The previously
flaky `TestToolCallTranslationSequence` passed 20/20 with `-race -count=20`;
full suite passed 3/3 consecutive runs.

AC → test mapping:

- AC 1-2: `TestTransportRequestResponseCorrelation`,
  `TestTransportStringIDCorrelation`, `TestRequestIDRoundTrip`.
- AC 3: `TestConcurrentOutboundCalls` (8 concurrent calls, out-of-order
  responses, correct correlation).
- AC 4: `TestTransportUnrecognizedMethod` (-32601),
  `TestTransportMalformedLineDoesNotKillConnection`.
- AC 5: `TestTransportRequestResponseCorrelation` (asserts protocolVersion 1,
  empty authMethods, text-only prompt capabilities, no http/sse MCP).
- AC 6-7: `TestSessionLifecycleEndToEnd`, `TestSessionNewStdioMCPServer`,
  `TestSessionNewHTTPMCPServerRejected` (-32602).
- AC 8: New() failure path covered indirectly (error mapping code in
  `handleNewSession`); CLINotFound/CLIConnectionError surface as -32603.
- AC 9-10: `TestSessionLifecycleEndToEnd` (prompt → streamed updates →
  end_turn response after terminal message),
  `TestContentBlockRejectsNonText` (invalid_params path).
- AC 11: `TestSessionLifecycleEndToEnd` (end_turn),
  `TestSessionCancelMidTurn` (cancelled), `TestPermissionDenyFlow`
  (refusal via PermissionDenials).
- AC 12-13: `TestSessionCancelMidTurn`, `TestSessionCancelPendingPermission`
  (pending permission resolved by cancel, prompt returns cancelled).
- AC 14: `$/cancel_request` is not registered; connection ignores it like any
  unknown notification (no response, per JSON-RPC).
- AC 15: `TestToolCallTranslationSequence` (pending → in_progress →
  completed with content, asserted in order), `TestToolCallFailedStatus`
  (failed), `TestThinkingBlockBecomesAgentThoughtChunk`,
  `TestSessionUpdateMarshalRoundTrip`.
- AC 16: `TestMapToolKind` (full table incl. `mcp__myserver__mytool` → other),
  title assertions in `TestToolCallTranslationSequence`.
- AC 17-19: `TestPermissionAllowFlow` (request shape, binary options, tool
  completes), `TestPermissionDenyFlow`, `TestSessionCancelPendingPermission`,
  `TestConcurrentOutboundCalls`.
