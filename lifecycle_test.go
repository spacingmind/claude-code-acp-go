package acp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// --- session-fixture helpers ---
//
// The SDK's session functions read local .jsonl transcripts directly from
// <config home>/projects/<sanitized-project-dir>/, not through the CLI
// subprocess, so tests here write fixtures under a temp config home and
// point the SDK's config-home resolver at it (mirroring
// claude-agent-sdk-go's own fakeConfigHome pattern).

// setFakeConfigHome points the SDK's session store at a fresh temp
// directory. The SDK resolves its config home from $CLAUDE_CONFIG_DIR on
// every call (it's an env-read, not a cached value), so t.Setenv is
// enough -- no need for an exported test hook.
func setFakeConfigHome(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	return dir
}

// fakeProject creates a real work directory plus its sanitized projects/
// storage directory, like the SDK's own test helper.
func fakeProject(t *testing.T, configHome, name string) (projectDir, storageDir string) {
	t.Helper()

	projectDir = filepath.Join(configHome, "workspaces", name)
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	storageDir = filepath.Join(configHome, "projects", sanitize(projectDir))
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}

	return projectDir, storageDir
}

// sanitize replicates the SDK's sanitizeProjectDirName so fixtures land
// where the SDK will look for them.
// sanitize replicates the CLI's project-storage directory naming: real
// path with every non-[A-Za-z0-9] rune replaced by '-'. (The SDK's own
// version also hash-truncates names over 200 chars, which never applies
// to these temp-dir fixtures.)
func sanitize(projectDir string) string {
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}

	var b strings.Builder

	for _, r := range projectDir {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}

	return b.String()
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func userEntry(uuid, parent, cwd, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"parentUuid":%q,"sessionId":"sid","cwd":%q,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`, uuid, parent, cwd, text)
}

func assistantToolEntry(uuid, parent, blocks string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"parentUuid":%q,"sessionId":"sid","message":{"role":"assistant","content":[%s]}}`, uuid, parent, blocks)
}

const (
	uuidA = "11111111-1111-1111-1111-111111111111"
	uuidB = "22222222-2222-2222-2222-222222222222"
)

// --- A. capability advertisement ---

func TestInitializeCapabilitiesPhase2(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(1, MethodInitialize, InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatal(err)
	}

	var init InitializeResponse
	if err := json.Unmarshal(resp.Result, &init); err != nil {
		t.Fatal(err)
	}

	if !init.AgentCapabilities.LoadSession {
		t.Error("loadSession should be true")
	}

	if !init.AgentCapabilities.SessionCapabilities.List || !init.AgentCapabilities.SessionCapabilities.Resume {
		t.Errorf("sessionCapabilities = %+v, want list+resume", init.AgentCapabilities.SessionCapabilities)
	}

	if init.AgentCapabilities.SessionCapabilities.Delete {
		t.Error("sessionCapabilities.delete should stay false")
	}
}

func TestNewSessionModes(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse
	if err := json.Unmarshal(resp.Result, &ns); err != nil {
		t.Fatal(err)
	}

	if ns.Modes == nil || ns.Modes.CurrentModeID != "default" {
		t.Fatalf("modes = %+v, want currentModeId default", ns.Modes)
	}

	if len(ns.Modes.AvailableModes) != 4 || ns.Modes.AvailableModes[0].ID != "default" || ns.Modes.AvailableModes[3].ID != "plan" {
		t.Errorf("availableModes = %+v", ns.Modes.AvailableModes)
	}
}

// --- B. session/load ---

func TestSessionLoadReplaysHistory(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir, storage := fakeProject(t, home, "proj")

	writeLines(t, filepath.Join(storage, uuidA+".jsonl"),
		userEntry(uuidA, "", projectDir, "what is 2+2"),
		assistantToolEntry(uuidB, uuidA, `{"type":"text","text":"it is 4"}`),
	)

	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(1, MethodLoadSession, LoadSessionRequest{SessionID: uuidA, CWD: projectDir})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("session/load error: %+v", resp.Error)
	}

	var lr LoadSessionResponse
	if err := json.Unmarshal(resp.Result, &lr); err != nil {
		t.Fatal(err)
	}

	if lr.Modes == nil || lr.Modes.CurrentModeID != "default" || len(lr.Modes.AvailableModes) != 4 {
		t.Errorf("load modes = %+v", lr.Modes)
	}

	updates := h.waitForUpdates(uuidA, 2)
	if updates[0].Update.UserMessageChunk == nil || updates[0].Update.UserMessageChunk.Content.Text.Text != "what is 2+2" {
		t.Errorf("update[0] = %+v, want user_message_chunk", updates[0].Update)
	}

	if updates[1].Update.AgentMessageChunk == nil || updates[1].Update.AgentMessageChunk.Content.Text.Text != "it is 4" {
		t.Errorf("update[1] = %+v, want agent_message_chunk", updates[1].Update)
	}

	// A subsequent prompt on the loaded session works like a normal one.
	presp, err := h.call(2, MethodPrompt, PromptRequest{SessionID: uuidA, Prompt: promptBlocks("again").Prompt})
	if err != nil {
		t.Fatal(err)
	}

	if presp.Error != nil {
		t.Fatalf("session/prompt after load: %+v", presp.Error)
	}
}

func TestSessionLoadReplaysToolUseAsText(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir, storage := fakeProject(t, home, "proj")

	writeLines(t, filepath.Join(storage, uuidA+".jsonl"),
		userEntry(uuidA, "", projectDir, "run tests"),
		assistantToolEntry(uuidB, uuidA, `{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"go test ./..."}}`),
	)

	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	if _, err := h.call(1, MethodLoadSession, LoadSessionRequest{SessionID: uuidA, CWD: projectDir}); err != nil {
		t.Fatal(err)
	}

	updates := h.waitForUpdates(uuidA, 2)

	if updates[1].Update.AgentMessageChunk == nil || !strings.Contains(updates[1].Update.AgentMessageChunk.Content.Text.Text, "Bash") {
		t.Errorf("historical tool_use = %+v, want plain agent text describing the tool", updates[1].Update)
	}

	for _, u := range updates {
		if u.Update.ToolCall != nil || u.Update.ToolCallUpdate != nil {
			t.Errorf("history replay must not re-announce tool calls: %+v", u.Update)
		}
	}
}

func TestSessionLoadNotFound(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir, _ := fakeProject(t, home, "proj")

	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodLoadSession, LoadSessionRequest{SessionID: uuidA, CWD: projectDir})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error == nil || resp.Error.Code != CodeResourceNotFound {
		t.Fatalf("want -32002, got %+v", resp.Error)
	}
}

func TestSessionLoadRejectsHTTPMcpServer(t *testing.T) {
	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodLoadSession, LoadSessionRequest{
		SessionID:  uuidA,
		CWD:        t.TempDir(),
		McpServers: []McpServer{{HTTP: &McpServerHTTP{Name: "x", URL: "http://example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want invalid_params, got %+v", resp.Error)
	}
}

// --- C. session/resume ---

func TestSessionResumeNoHistoryReplay(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir, storage := fakeProject(t, home, "proj")

	writeLines(t, filepath.Join(storage, uuidA+".jsonl"),
		userEntry(uuidA, "", projectDir, "hello"),
		assistantToolEntry(uuidB, uuidA, `{"type":"text","text":"hi"}`),
	)

	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(1, MethodResumeSession, ResumeSessionRequest{SessionID: uuidA, CWD: projectDir})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("session/resume error: %+v", resp.Error)
	}

	var rr ResumeSessionResponse
	if err := json.Unmarshal(resp.Result, &rr); err != nil {
		t.Fatal(err)
	}

	if rr.Modes == nil || rr.Modes.CurrentModeID != "default" {
		t.Errorf("resume modes = %+v", rr.Modes)
	}

	if got := h.updatesSnapshot(); len(got) != 0 {
		t.Fatalf("session/resume must not replay history; got %d updates", len(got))
	}
}

func TestSessionResumeNotFound(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir, _ := fakeProject(t, home, "proj")

	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodResumeSession, ResumeSessionRequest{SessionID: uuidA, CWD: projectDir})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error == nil || resp.Error.Code != CodeResourceNotFound {
		t.Fatalf("want -32002, got %+v", resp.Error)
	}
}

// --- D. session/list ---

func TestSessionListAllProjectsAndScoped(t *testing.T) {
	home := setFakeConfigHome(t)
	projectDir1, storage1 := fakeProject(t, home, "p1")
	projectDir2, storage2 := fakeProject(t, home, "p2")

	writeLines(t, filepath.Join(storage1, uuidA+".jsonl"), userEntry(uuidA, "", projectDir1, "hello from p1"))
	writeLines(t, filepath.Join(storage2, uuidB+".jsonl"), userEntry(uuidB, "", projectDir2, "hello from p2"))

	h := newACPHarness(t, nil)

	// cwd unset: all projects.
	resp, err := h.call(1, MethodListSessions, ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	var all ListSessionsResponse
	if err := json.Unmarshal(resp.Result, &all); err != nil {
		t.Fatal(err)
	}

	if len(all.Sessions) != 2 {
		t.Fatalf("all-projects list = %+v, want 2 sessions", all.Sessions)
	}

	for _, s := range all.Sessions {
		if s.CWD == "" {
			t.Errorf("session %s has empty cwd", s.SessionID)
		}
	}

	if all.NextCursor != nil {
		t.Error("nextCursor must be absent")
	}

	// cwd set: scoped to one project.
	resp, err = h.call(2, MethodListSessions, ListSessionsRequest{CWD: projectDir1})
	if err != nil {
		t.Fatal(err)
	}

	var scoped ListSessionsResponse
	if err := json.Unmarshal(resp.Result, &scoped); err != nil {
		t.Fatal(err)
	}

	if len(scoped.Sessions) != 1 || scoped.Sessions[0].SessionID != uuidA || scoped.Sessions[0].CWD != projectDir1 {
		t.Fatalf("scoped list = %+v", scoped.Sessions)
	}

	if scoped.Sessions[0].Title == nil || *scoped.Sessions[0].Title != "hello from p1" {
		t.Errorf("title = %v, want summary fallback", scoped.Sessions[0].Title)
	}

	_ = projectDir2
}

func TestSessionListCursorIgnored(t *testing.T) {
	setFakeConfigHome(t)

	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodListSessions, ListSessionsRequest{Cursor: "opaque-cursor"})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("cursor must be accepted and ignored, got %+v", resp.Error)
	}

	var lr ListSessionsResponse
	if err := json.Unmarshal(resp.Result, &lr); err != nil {
		t.Fatal(err)
	}

	if lr.Sessions == nil || len(lr.Sessions) != 0 {
		t.Errorf("sessions = %+v, want empty non-nil", lr.Sessions)
	}
}

func TestSessionListEmptyConfigDir(t *testing.T) {
	setFakeConfigHome(t)

	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodListSessions, ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("empty config dir must not error, got %+v", resp.Error)
	}

	var lr ListSessionsResponse
	if err := json.Unmarshal(resp.Result, &lr); err != nil {
		t.Fatal(err)
	}

	if len(lr.Sessions) != 0 {
		t.Errorf("sessions = %+v, want empty", lr.Sessions)
	}
}

// --- E. session/set_mode ---

func TestSessionSetMode(t *testing.T) {
	modeFile := filepath.Join(t.TempDir(), "mode.txt")

	// WithEnv replaces (not merges), so all fake-CLI env vars must ride
	// one WithEnv call alongside the harness's own.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	opts := []claudecode.Option{
		claudecode.WithCLIPath(self),
		claudecode.WithEnv(
			"ACP_FAKE_CLI=1",
			"ACP_FAKE_SCENARIO=await_control",
			"ACP_FAKE_MODE_FILE="+modeFile,
		),
	}

	h := newACPHarness(t, opts)

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	resp, err := h.call(2, MethodSetSessionMode, SetSessionModeRequest{SessionID: ns.SessionID, ModeID: "acceptEdits"})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error != nil {
		t.Fatalf("session/set_mode: %+v", resp.Error)
	}

	// Empty success result.
	var result SetSessionModeResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("set_mode result should decode as empty object: %v (raw %s)", err, resp.Result)
	}

	// The CLI observably received the set_permission_mode control request.
	deadline := time.Now().Add(10 * time.Second)

	for {
		if b, err := os.ReadFile(modeFile); err == nil && string(b) == "acceptEdits" {
			break
		}

		if time.Now().After(deadline) {
			t.Fatalf("fake cli never recorded set_permission_mode=acceptEdits (file content: %q)", readOrEmpty(modeFile))
		}

		time.Sleep(10 * time.Millisecond)
	}

	updates := h.waitForUpdates(ns.SessionID, 1)
	if updates[0].Update.CurrentModeUpdate == nil || updates[0].Update.CurrentModeUpdate.CurrentModeID != "acceptEdits" {
		t.Fatalf("update = %+v, want current_mode_update acceptEdits", updates[0].Update)
	}
}

func readOrEmpty(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	return string(b)
}

func TestSessionSetModeUnknownMode(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	resp, err := h.call(2, MethodSetSessionMode, SetSessionModeRequest{SessionID: ns.SessionID, ModeID: "turbo"})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want invalid_params, got %+v", resp.Error)
	}
}

func TestSessionSetModeUnknownSession(t *testing.T) {
	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodSetSessionMode, SetSessionModeRequest{SessionID: "nope", ModeID: "plan"})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Error == nil || resp.Error.Code != CodeResourceNotFound {
		t.Fatalf("want -32002, got %+v", resp.Error)
	}
}
