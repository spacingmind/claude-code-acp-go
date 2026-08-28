# ACP Session Lifecycle (phase 2)

Closes the 4 methods phase 1 deferred: `session/load`, `session/resume`,
`session/list`, `session/set_mode`. Unlike phase 1 (a from-scratch
transport + translation layer), this phase is mostly thin wrappers over
`claude-agent-sdk-go` functionality that already exists: local session
resume (`WithResume`) and local-disk listing/reading (`ListSessions`,
`GetSessionInfo`, `GetSessionMessages`) from that repo's completed
`session-lifecycle-local-resume` plan, plus `Client.SetPermissionMode`
from its core control protocol.

## Research basis

Already captured in phase 1's research pass (`docs/plans/completed/acp-agent-core.md`),
section 3 of that plan's research: `LoadSessionRequest`/`LoadSessionResponse`,
`ResumeSessionRequest`/`ResumeSessionResponse`, `ListSessionsRequest`/`ListSessionsResponse`/`SessionInfo`,
`SetSessionModeRequest`/`SetSessionModeResponse`/`SessionMode`/`SessionModeState` —
no new schema research needed, just implementation.

## Acceptance Criteria

### A. Capability advertisement

1. `initialize`'s `AgentCapabilities` now reports `LoadSession: true` and
   `SessionCapabilities{List: true, Resume: true}` (`Delete`/`Close` stay
   unsupported — no ACP method for them implemented, matches phase 1's
   scope boundary).
2. `session/new`, `session/load`, and `session/resume` responses all
   include `Modes: &SessionModeState{CurrentModeID: "default", AvailableModes: [...]}`
   — this is new: phase 1 didn't advertise modes at all.
   `AvailableModes` is a fixed list of `SessionMode{ID, Name}` mirroring
   Claude Code's own permission modes: `{ID:"default",Name:"Default"}`,
   `{ID:"acceptEdits",Name:"Accept Edits"}`, `{ID:"bypassPermissions",Name:"Bypass Permissions"}`,
   `{ID:"plan",Name:"Plan"}`. `CurrentModeID` reflects whatever permission
   mode the session's underlying `Client` was actually constructed with
   (default `"default"` unless the session was created some other way —
   there's no ACP-side way to choose it at `session/new` time yet, so it's
   always `"default"` there; `session/load`/`session/resume` are the same
   since neither carries a mode selector in their request either).

### B. `session/load`

3. Handles `{"method":"session/load","params":{"sessionId":"...","cwd":"...","mcpServers":[...]}}`.
   `mcpServers` validated the same way `session/new` already does (stdio
   only, reject http/sse with `invalid_params`). Constructs a
   `claude-agent-sdk-go` `Client` via `New(cwd, claudecode.WithResume(sessionId), <mcp options>...)`.
4. Before returning the response, replays the session's prior visible
   history as `session/update` notifications: call
   `claudecode.GetSessionMessages(sessionId, cwd, 0, 0)` (no limit/offset —
   full history) and translate each returned `SessionMessage` into
   `agent_message_chunk`/`user_message_chunk` updates (reuse as much of
   `translate.go`'s existing block-translation logic as makes sense —
   `SessionMessage.Message` is `json.RawMessage` holding the raw Anthropic
   API message object, not a `claudecode.Message`, so this needs its own
   thin unmarshal-then-translate path, not a direct reuse of
   `translateMessage`). Tool calls from history are NOT re-announced as
   live `tool_call`/`tool_call_update` sequences (they already happened) —
   represent historical tool use as plain content within the replayed
   chunks if reasonably simple to do, or skip tool-use content from
   history replay entirely if that turns out to be substantially simpler
   and still gives a reasonable transcript — implementer's call, document
   whichever is chosen.
5. Responds with `LoadSessionResponse{Modes: <per AC 2>}` (no
   `ConfigOptions`). If the session ID doesn't correspond to any existing
   local session (`GetSessionMessages` returns empty/an error indicating
   not-found), return a `-32002` resource-not-found error instead of
   silently creating an empty session.
6. Registers the loaded session in the same `(sessionID → *session)` map
   `session/new` uses — after `session/load` succeeds, `session/prompt`
   with this `sessionId` works exactly like a `session/new`-created one
   (same cancel/permission-request plumbing from phase 1, no special-
   casing needed there).

### C. `session/resume`

7. Handles `{"method":"session/resume","params":{"sessionId":"...","cwd":"...","additionalDirectories"?:[...],"mcpServers"?:[...]}}`.
   Same MCP-server validation and `Client` construction as `session/load`
   (`WithResume(sessionId)`), but **no history replay** — no
   `session/update` notifications sent during this call, matching the ACP
   spec's explicit distinction ("resumes... without returning previous
   messages, unlike `session/load`"). `additionalDirectories` — accept but
   don't need to wire to anything in this phase (no existing SDK option
   consumes it yet; note as a no-op in a code comment, don't error on it).
8. Responds with `ResumeSessionResponse{Modes: <per AC 2>}`. Same
   resource-not-found handling as AC 5, same session-map registration as
   AC 6.

### D. `session/list`

9. Handles `{"method":"session/list","params":{"cwd"?:"...","cursor"?:"..."}}`.
   Maps directly to `claudecode.ListSessions(claudecode.ListSessionsOptions{Directory: <cwd, or "" if absent — "" means all projects per the SDK's existing semantics>})`.
   No pagination support in this phase — `cursor` is accepted but ignored,
   `nextCursor` is never returned (always `nil`/absent). If the caller
   passes a `cursor` we don't understand, don't error — just ignore it and
   return the first page (which is everything, since we don't paginate).
10. Translates each `claudecode.SDKSessionInfo` to ACP's `SessionInfo{SessionID, CWD, Title?, UpdatedAt?}`:
    `SessionID` ← `SDKSessionInfo.SessionID`, `CWD` ← `SDKSessionInfo.Cwd`
    (required by the ACP schema — if the SDK's value is empty, fall back
    to the `cwd` from the request, or omit the session from the list
    entirely if truly nothing usable is available, documenting whichever
    choice is made), `Title` ← `SDKSessionInfo.CustomTitle` if non-empty
    else `SDKSessionInfo.Summary` if non-empty else omitted, `UpdatedAt` ←
    `SDKSessionInfo.LastModified` (epoch ms) formatted as ISO 8601 (the
    ACP schema field is a string).

### E. `session/set_mode`

11. Handles `{"method":"session/set_mode","params":{"sessionId":"...","modeId":"..."}}`.
    Looks up the session's `Client` (unknown ID → `-32002`), validates
    `modeId` is one of the 4 fixed IDs from AC 2 (unknown mode →
    `invalid_params`), calls `Client.SetPermissionMode(ctx, modeId)`.
12. On success, sends a `session/update` notification with
    `SessionUpdate{CurrentModeUpdate: &CurrentModeUpdate{CurrentModeID: modeId}}`
    (per the ACP schema — confirming the change) **and** responds to the
    `session/set_mode` request itself with an empty
    `SetSessionModeResponse{}`. Order between the notification and the
    response doesn't matter per the spec, but send both.
13. Track the session's current mode ID in the session state (so a
    subsequent `session/load`/`session/resume`/`session/new` response's
    `Modes.CurrentModeID` could in principle reflect it if the same
    session were re-queried — not required by any test in this phase, but
    don't make it structurally impossible to add later).

## Test Scenarios

- `initialize` response now has `LoadSession: true`,
  `SessionCapabilities{List:true, Resume:true}`.
- `session/new` response includes `Modes` with `CurrentModeID: "default"`
  and the 4 fixed `AvailableModes`.
- `session/load` for a session with existing local history (write fixture
  `.jsonl` files under a temp config-home directory, matching
  `claude-agent-sdk-go`'s existing `sessions_test.go` fixture patterns) →
  observe `session/update` replay notifications before the response, then
  a subsequent `session/prompt` on that session works normally.
- `session/load` for a nonexistent session ID → `-32002`.
- `session/resume` for an existing session → response arrives with no
  preceding `session/update` notifications (assert zero updates received
  before the response, contrasting with `session/load`'s replay).
- `session/list` with `cwd` unset → returns sessions from a multi-project
  fixture (across 2+ project directories) — mirrors
  `TestListSessions_AllProjectsWhenDirectoryEmpty` from
  `claude-agent-sdk-go`. With `cwd` set → scoped to one project.
- `session/list` against an empty/nonexistent config directory → empty
  `sessions` array, not an error.
- `session/set_mode` with a valid `modeId` → `Client.SetPermissionMode`
  observably called with the right value (assert via the fake-CLI
  harness's captured control-request traffic, matching
  `claude-agent-sdk-go`'s own `TestClient_OutboundControlWireShapes`
  pattern) + a `current_mode_update` notification is sent + the request
  gets an empty success response.
- `session/set_mode` with an unrecognized `modeId` → `invalid_params`.
- `session/set_mode` on an unknown `sessionId` → `-32002`.

## Decisions

- **No pagination for `session/list`** — matches the "keep it minimal"
  spirit of phase 1; `claude-agent-sdk-go`'s `ListSessions` doesn't
  naturally paginate either (it returns a full slice), so there's nothing
  cheap to wire up here without inventing cursor semantics that don't
  exist on the underlying SDK.
- **Fixed 4-mode set for `session/set_mode`**, mirroring Claude Code's own
  permission modes exactly — no generic/extensible mode system. If a
  richer mode concept is ever needed, it's a bigger design question for a
  future phase, not a natural extension of this one.
- **History replay implementation detail (AC 4) left to the implementer's
  judgment** on exactly how much of historical tool-use content to
  reconstruct — the plan doc flags this explicitly rather than
  prescribing one approach, since the "right" level of fidelity here is a
  reasonable judgment call, not a wire-protocol correctness question.

## Progress

Complete. Implemented on `main`:

- **AC 1-2** (`types.go`, `agent.go`): `initialize` now reports
  `LoadSession: true` and `SessionCapabilities{List: true, Resume: true}`;
  `SessionModeState` + fixed 4-mode `availableModes()` helper added and
  wired into `session/new`/`session/load`/`session/resume` responses.
- **AC 3-6** (`agent.go` `handleLoadSession`, `translate.go`
  `replayHistory`): existence check via `GetSessionMessages` first
  (empty/error → `-32002`), then `Client` built via the shared
  `newSessionClient` helper (factored out of `session/new`, carrying the
  stdio-only MCP validation) with `WithResume(sessionId)`, history
  replayed as `session/update` notifications before responding, session
  registered in the shared map.
- **AC 4 fidelity call (deviation documented):** historical `tool_use`
  blocks are replayed as plain agent-message text chunks
  (`[tool: <title>]`) using the existing `toolTitle` logic — chosen over
  re-announcing live `tool_call`/`tool_call_update` sequences because the
  calls already completed and the ACP tool-call UI belongs to the current
  turn. Tool results and thinking blocks are skipped. The choice is
  documented in `replayHistory`'s doc comment.
- **AC 7-8** (`handleResumeSession`): same construction/validation/
  existence check, no replay; `additionalDirectories` accepted as a
  no-op (noted in a comment).
- **AC 9-10** (`handleListSessions`): `cursor` accepted and ignored,
  `nextCursor` never set; `SDKSessionInfo` → `SessionInfo` translation
  with `CWD` required-field fallback (SDK cwd → request cwd → session
  omitted) and `Title` derivation (CustomTitle → Summary → omitted);
  `UpdatedAt` rendered as ISO-8601 UTC from `LastModified` epoch ms.
- **AC 11-13** (`handleSetSessionMode`): fixed 4-mode validation,
  `Client.SetPermissionMode`, `current_mode_update` notification, empty
  `SetSessionModeResponse{}`; current mode tracked on the `session`
  struct behind its existing mutex.
- **Types** (`types.go`): `SessionMode`, `SessionModeState`,
  `LoadSessionRequest/Response`, `ResumeSessionRequest/Response`,
  `ListSessionsRequest/Response`, `SessionInfo`,
  `SetSessionModeRequest/Response`, and the `CurrentModeUpdate` variant
  added to the `SessionUpdate` tagged union following the existing
  marshal/unmarshal pattern.
- **Tests** (`lifecycle_test.go`): fixture helpers writing `.jsonl`
  session transcripts under a temp `$CLAUDE_CONFIG_DIR` (via `t.Setenv`
  — the SDK re-reads the env var per call, so no test hook needed),
  mirroring `claude-agent-sdk-go`'s `sessions_test.go` patterns; fake-CLI
  gained an `await_control` scenario that records received
  `set_permission_mode` control requests to a file for wire-level
  assertion. One pre-existing test updated: phase 1's
  `TestTransportUnrecognizedMethod` used `session/load` as its
  not-yet-registered example method.

## Validation

All commands run on the final tree:

- `go build -buildvcs=false ./...` — clean.
- `go test -race -count=1 -timeout 300s ./...` — `ok` (13.1s); full
  suite run 3 consecutive times, all green, no flakes.
- `go vet ./...` — clean.
- `gofmt -l .` — empty.
- `golangci-lint run` — 0 issues (two exclusions widened in
  `.golangci.yml`: gosec G304/G703 for `_test.go` fixture paths under
  `t.TempDir()`, alongside the pre-existing G301/G306 exclusion).
- Zero leaked fake-CLI processes after the suite (`pgrep` clean).

Every Test Scenario in the plan is covered:

1. `TestInitializeCapabilitiesPhase2` — LoadSession +
   SessionCapabilities{List, Resume}.
2. `TestNewSessionModes` — Modes with CurrentModeID `default` + 4 fixed
   AvailableModes.
3. `TestSessionLoadReplaysHistory` — replay notifications before the
   response, then `session/prompt` works on the loaded session.
   `TestSessionLoadReplaysToolUseAsText` — historical tool use as plain
   text, never re-announced as tool_call updates.
4. `TestSessionLoadNotFound` — `-32002`.
5. `TestSessionResumeNoHistoryReplay` — zero updates before the
   response. `TestSessionResumeNotFound` — `-32002`.
6. `TestSessionListAllProjectsAndScoped` — multi-project fixture,
   unscoped returns both, cwd-scoped returns one, titles derived.
   `TestSessionListEmptyConfigDir` / `TestSessionListCursorIgnored` —
   empty non-error list; cursor ignored.
7. `TestSessionSetMode` — `SetPermissionMode` observed on the fake-CLI
   control wire (mode file), `current_mode_update` notification, empty
   success response.
8. `TestSessionSetModeUnknownMode` — `invalid_params`.
9. `TestSessionSetModeUnknownSession` — `-32002`.
