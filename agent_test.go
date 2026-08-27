package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Transport scenarios ---

func TestTransportRequestResponseCorrelation(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(42, MethodInitialize, InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}

	var init InitializeResponse
	if err := json.Unmarshal(resp.Result, &init); err != nil {
		t.Fatalf("unmarshal init: %v", err)
	}

	if init.ProtocolVersion != 1 {
		t.Errorf("protocolVersion = %d, want 1", init.ProtocolVersion)
	}

	if len(init.AuthMethods) != 0 {
		t.Errorf("authMethods = %v, want empty", init.AuthMethods)
	}

	if init.AgentCapabilities.PromptCapabilities.Image {
		t.Error("promptCapabilities.image should be false")
	}
}

func TestTransportStringIDCorrelation(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call("string-id-1", MethodInitialize, InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}

	var wantID RequestID
	if err := json.Unmarshal([]byte(`"string-id-1"`), &wantID); err != nil {
		t.Fatal(err)
	}

	if *resp.ID != wantID {
		t.Errorf("response id = %v, want string \"string-id-1\"", resp.ID)
	}
}

func TestTransportUnrecognizedMethod(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(7, "session/load", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("expected error response for unrecognized method")
	}

	if resp.Error.Code != CodeMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, CodeMethodNotFound)
	}
}

func TestTransportMalformedLineDoesNotKillConnection(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	h.writeRaw("this is not json")
	h.writeRaw(`{"jsonrpc":"1.0","id":1,"method":"foo"}`) // wrong version: dropped

	// Connection must still work.
	resp, err := h.call(9, MethodInitialize, InitializeRequest{ProtocolVersion: 1})
	if err != nil {
		t.Fatalf("initialize after malformed lines: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
}

// --- Session lifecycle scenarios ---

func promptBlocks(text string) PromptRequest {
	return PromptRequest{
		Prompt: []ContentBlock{{Text: &TextContent{Type: "text", Text: text}}},
	}
}

func TestSessionLifecycleEndToEnd(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	if _, err := h.call(1, MethodInitialize, InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.call(2, MethodNewSession, NewSessionRequest{
		CWD:        t.TempDir(),
		McpServers: []McpServer{},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("session/new error: %+v", resp.Error)
	}

	var ns NewSessionResponse
	if err := json.Unmarshal(resp.Result, &ns); err != nil {
		t.Fatal(err)
	}

	if ns.SessionID == "" {
		t.Fatal("empty sessionId")
	}

	presp, err := h.call(3, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("hello").Prompt})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	if presp.Error != nil {
		t.Fatalf("session/prompt error: %+v", presp.Error)
	}

	var pr PromptResponse
	if err := json.Unmarshal(presp.Result, &pr); err != nil {
		t.Fatal(err)
	}

	if pr.StopReason != StopReasonEndTurn {
		t.Errorf("stopReason = %q, want end_turn", pr.StopReason)
	}

	updates := h.waitForUpdates(ns.SessionID, 1)
	if len(updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(updates))
	}

	chunk := updates[0].Update.AgentMessageChunk
	if chunk == nil || chunk.Content.Text == nil || chunk.Content.Text.Text != "hello there" {
		t.Errorf("unexpected update: %+v", updates[0].Update)
	}
}

func TestSessionNewStdioMCPServer(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	if _, err := h.call(1, MethodInitialize, InitializeRequest{ProtocolVersion: 1}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.call(2, MethodNewSession, NewSessionRequest{
		CWD: t.TempDir(),
		McpServers: []McpServer{{Stdio: &McpServerStdio{
			Name:    "myserver",
			Command: "some-mcp-binary",
			Args:    []string{"--flag"},
			Env:     []string{"FOO=bar"},
		}}},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if resp.Error != nil {
		t.Fatalf("stdio mcp server should be accepted, got error: %+v", resp.Error)
	}

	var ns NewSessionResponse
	if err := json.Unmarshal(resp.Result, &ns); err != nil {
		t.Fatal(err)
	}

	if ns.SessionID == "" {
		t.Fatal("empty sessionId")
	}
}

func TestSessionNewHTTPMCPServerRejected(t *testing.T) {
	h := newACPHarness(t, nil)

	resp, err := h.call(1, MethodNewSession, NewSessionRequest{
		CWD: t.TempDir(),
		McpServers: []McpServer{{HTTP: &McpServerHTTP{
			Name: "remote",
			URL:  "https://example.invalid/mcp",
		}}},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("expected error for http mcp server")
	}

	if resp.Error.Code != CodeInvalidParams {
		t.Errorf("code = %d, want %d (invalid_params)", resp.Error.Code, CodeInvalidParams)
	}
}

func TestPromptUnknownSession(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "simple"))

	resp, err := h.call(1, MethodPrompt, PromptRequest{SessionID: "nope", Prompt: promptBlocks("x").Prompt})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	if resp.Error == nil {
		t.Fatal("expected error for unknown session")
	}

	if resp.Error.Code != CodeResourceNotFound {
		t.Errorf("code = %d, want %d (resource not found)", resp.Error.Code, CodeResourceNotFound)
	}
}

// --- Tool call translation scenarios ---

func toolUpdates(t *testing.T, h *acpTestHarness, sessionID string, n int) []SessionUpdate {
	t.Helper()

	var out []SessionUpdate
	for _, un := range h.waitForUpdates(sessionID, n) {
		out = append(out, un.Update)
	}

	return out
}

func TestToolCallTranslationSequence(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "tool_turn"))
	h.servePermissions(func(_ RequestPermissionRequest) RequestPermissionResponse {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID: "allow"}}}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	presp, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("run tests").Prompt})
	if err != nil {
		t.Fatal(err)
	}

	if presp.Error != nil {
		t.Fatalf("session/prompt error: %+v", presp.Error)
	}

	// Expected sequence: tool_call(pending) -> in_progress -> completed(with content) -> agent_message_chunk("all done")
	updates := toolUpdates(t, h, ns.SessionID, 4)
	if len(updates) != 4 {
		t.Fatalf("updates = %d (%+v), want 4", len(updates), updates)
	}

	tc := updates[0].ToolCall
	if tc == nil {
		t.Fatalf("updates[0] not a tool_call: %+v", updates[0])
	}

	if tc.ToolCallID != "tool-1" || tc.Kind != ToolKindExecute || tc.Status != ToolCallStatusPending {
		t.Errorf("tool_call = %+v", tc)
	}

	if !strings.HasPrefix(tc.Title, "Bash") {
		t.Errorf("title = %q, want Bash prefix", tc.Title)
	}

	ip := updates[1].ToolCallUpdate
	if ip == nil || ip.ToolCallID != "tool-1" || ip.Status == nil || *ip.Status != ToolCallStatusInProgress {
		t.Errorf("updates[1] not in_progress: %+v", updates[1])
	}

	done := updates[2].ToolCallUpdate
	if done == nil || done.Status == nil || *done.Status != ToolCallStatusCompleted {
		t.Fatalf("updates[2] not completed: %+v", updates[2])
	}

	if len(done.Content) != 1 || done.Content[0].Text == nil || done.Content[0].Text.Text != "go test ok" {
		t.Errorf("completed content = %+v", done.Content)
	}

	if updates[3].AgentMessageChunk == nil || updates[3].AgentMessageChunk.Content.Text.Text != "all done" {
		t.Errorf("updates[3] not agent_message_chunk: %+v", updates[3])
	}
}

func TestToolCallFailedStatus(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "tool_fail"))
	h.servePermissions(func(_ RequestPermissionRequest) RequestPermissionResponse {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID: "allow"}}}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	if _, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt}); err != nil {
		t.Fatal(err)
	}

	for _, u := range toolUpdates(t, h, ns.SessionID, 3) {
		if u.ToolCallUpdate != nil && u.ToolCallUpdate.Status != nil && *u.ToolCallUpdate.Status == ToolCallStatusFailed {
			return
		}
	}

	t.Fatal("no failed tool_call_update seen")
}

func TestMCPToolNameMapsToOtherKind(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "mcp_tool"))
	h.servePermissions(func(_ RequestPermissionRequest) RequestPermissionResponse {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID: "allow"}}}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	if _, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt}); err != nil {
		t.Fatal(err)
	}

	for _, u := range toolUpdates(t, h, ns.SessionID, 1) {
		if u.ToolCall != nil {
			if u.ToolCall.Kind != ToolKindOther {
				t.Errorf("kind = %q, want other", u.ToolCall.Kind)
			}

			return
		}
	}

	t.Fatal("no tool_call seen")
}

func TestThinkingBlockBecomesAgentThoughtChunk(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "thinking"))

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	if _, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt}); err != nil {
		t.Fatal(err)
	}

	updates := toolUpdates(t, h, ns.SessionID, 2)
	if updates[0].AgentThoughtChunk == nil || updates[0].AgentThoughtChunk.Content.Text.Text != "hmm, pondering" {
		t.Errorf("updates[0] not agent_thought_chunk: %+v", updates[0])
	}

	if updates[1].AgentMessageChunk == nil {
		t.Errorf("updates[1] not agent_message_chunk: %+v", updates[1])
	}
}

// --- Permission flow scenarios ---

func TestPermissionAllowFlow(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "tool_turn"))
	h.servePermissions(func(_ RequestPermissionRequest) RequestPermissionResponse {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID: "allow"}}}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	presp, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt})
	_ = presp

	if err != nil {
		t.Fatal(err)
	}

	if presp.Error != nil {
		t.Fatalf("session/prompt error: %+v", presp.Error)
	}

	// The permission request must have been sent with allow/deny options.
	deadline := time.Now().Add(10 * time.Second)
	for h.permCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	h.mu.Lock()
	reqs := append([]permCapture(nil), h.permReqs...)
	h.mu.Unlock()

	if len(reqs) != 1 {
		t.Fatalf("permission requests = %d, want 1", len(reqs))
	}

	pr := reqs[0]
	if pr.Req.SessionID != ns.SessionID {
		t.Errorf("permission sessionId = %q", pr.Req.SessionID)
	}

	if pr.Req.ToolCall.ToolCallID != "tool-1" {
		t.Errorf("permission toolCallId = %q", pr.Req.ToolCall.ToolCallID)
	}

	if len(pr.Req.Options) != 2 || pr.Req.Options[0].OptionID != "allow" || pr.Req.Options[1].OptionID != "deny" {
		t.Errorf("options = %+v", pr.Req.Options)
	}

	// Tool proceeded: completed update with output arrives.
	for _, u := range toolUpdates(t, h, ns.SessionID, 3) {
		if u.ToolCallUpdate != nil && u.ToolCallUpdate.Status != nil && *u.ToolCallUpdate.Status == ToolCallStatusCompleted {
			return
		}
	}

	t.Fatal("tool did not complete after allow")
}

func TestPermissionDenyFlow(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "deny_permission"))
	h.servePermissions(func(_ RequestPermissionRequest) RequestPermissionResponse {
		return RequestPermissionResponse{Outcome: RequestPermissionOutcome{Selected: &SelectedPermissionOutcome{OptionID: "deny"}}}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	presp, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt})
	if err != nil {
		t.Fatal(err)
	}

	if presp.Error != nil {
		t.Fatalf("session/prompt error: %+v", presp.Error)
	}

	var pr PromptResponse

	_ = json.Unmarshal(presp.Result, &pr)
	if pr.StopReason != StopReasonRefusal {
		t.Errorf("stopReason = %q, want refusal", pr.StopReason)
	}
}

// --- Cancellation scenarios ---

func TestSessionCancelMidTurn(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "interruptible"))

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	// Start the prompt asynchronously (it blocks until the turn ends).
	type promptResult struct {
		resp *Response
		err  error
	}

	ch := make(chan promptResult, 1)

	go func() {
		resp, err := h.call(3, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt})
		ch <- promptResult{resp, err}
	}()

	// Wait for the first streamed update, then cancel.
	h.waitForUpdates(ns.SessionID, 1)
	h.notify(MethodCancel, CancelNotification(ns))

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("session/prompt: %v", r.err)
		}

		if r.resp.Error != nil {
			t.Fatalf("session/prompt error: %+v", r.resp.Error)
		}

		var pr PromptResponse

		_ = json.Unmarshal(r.resp.Result, &pr)
		if pr.StopReason != StopReasonCancelled {
			t.Errorf("stopReason = %q, want cancelled", pr.StopReason)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for cancelled prompt response")
	}
}

func TestSessionCancelPendingPermission(t *testing.T) {
	h := newACPHarness(t, fakeCLIOptions(t, "hang_mid_permission"))

	// The permission responder never answers; cancellation must resolve it.
	answered := make(chan RequestPermissionRequest, 1)

	h.servePermissions(func(req RequestPermissionRequest) RequestPermissionResponse {
		answered <- req

		select {}
	})

	nsResp, err := h.call(1, MethodNewSession, NewSessionRequest{CWD: t.TempDir(), McpServers: []McpServer{}})
	if err != nil {
		t.Fatal(err)
	}

	var ns NewSessionResponse

	_ = json.Unmarshal(nsResp.Result, &ns)

	type promptResult struct {
		resp *Response
		err  error
	}

	ch := make(chan promptResult, 1)

	go func() {
		resp, err := h.call(2, MethodPrompt, PromptRequest{SessionID: ns.SessionID, Prompt: promptBlocks("x").Prompt})
		ch <- promptResult{resp, err}
	}()

	select {
	case <-answered:
	case <-time.After(30 * time.Second):
		t.Fatal("permission request never arrived")
	}

	h.notify(MethodCancel, CancelNotification(ns))

	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("session/prompt: %v", r.err)
		}

		if r.resp.Error != nil {
			t.Fatalf("session/prompt error: %+v", r.resp.Error)
		}

		var pr PromptResponse

		_ = json.Unmarshal(r.resp.Result, &pr)
		if pr.StopReason != StopReasonCancelled {
			t.Errorf("stopReason = %q, want cancelled", pr.StopReason)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("pending permission request was left hanging after session/cancel")
	}
}

// --- Unit tests for translation helpers ---

func TestMapToolKind(t *testing.T) {
	cases := map[string]ToolKind{
		"Read":                  ToolKindRead,
		"Edit":                  ToolKindEdit,
		"MultiEdit":             ToolKindEdit,
		"Write":                 ToolKindEdit,
		"NotebookEdit":          ToolKindEdit,
		"Bash":                  ToolKindExecute,
		"BashOutput":            ToolKindExecute,
		"Grep":                  ToolKindSearch,
		"Glob":                  ToolKindSearch,
		"WebFetch":              ToolKindFetch,
		"WebSearch":             ToolKindFetch,
		"mcp__myserver__mytool": ToolKindOther,
		"Task":                  ToolKindOther,
	}
	for name, want := range cases {
		if got := mapToolKind(name); got != want {
			t.Errorf("mapToolKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestSessionUpdateMarshalRoundTrip(t *testing.T) {
	u := SessionUpdate{AgentMessageChunk: &ContentChunk{Content: ContentBlock{Text: &TextContent{Type: "text", Text: "hi"}}}}

	data, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(data), `"sessionUpdate":"agent_message_chunk"`) {
		t.Errorf("marshaled update missing variant tag: %s", data)
	}

	var back SessionUpdate
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}

	if back.AgentMessageChunk == nil || back.AgentMessageChunk.Content.Text.Text != "hi" {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	for _, raw := range []string{`null`, `17`, `"abc"`, `12345678901234567890`} {
		var id RequestID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}

		out, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}

		if raw == `12345678901234567890` {
			// Out-of-int64-range numbers keep their exact textual value.
			if string(out) != raw {
				t.Errorf("id %s round tripped to %s", raw, out)
			}

			continue
		}

		if string(out) != raw {
			t.Errorf("id %s round tripped to %s", raw, out)
		}
	}
}

func TestContentBlockRejectsNonText(t *testing.T) {
	var c ContentBlock

	err := json.Unmarshal([]byte(`{"type":"image","data":"..."}`), &c)
	if err == nil {
		t.Fatal("expected error for non-text content block")
	}
}

func TestConcurrentOutboundCalls(t *testing.T) {
	// Transport-level concurrency: two Connections facing each other over
	// pipes; one fires several concurrent Calls, the other answers out of
	// order (first request answered last). Each call must get its own
	// correctly-correlated response.
	c1Reader, c1Writer := io.Pipe() // c1 writes requests here
	c2Reader, c2Writer := io.Pipe() // c2 writes responses here

	c1 := NewConnection(c2Reader, c1Writer) // c1 reads responses, writes requests
	c2 := NewConnection(c1Reader, c2Writer)

	t.Cleanup(func() {
		_ = c1Reader.Close()
		_ = c1Writer.Close()
		_ = c2Reader.Close()
		_ = c2Writer.Close()
	})

	var mu sync.Mutex

	seen := 0

	c2.RegisterRequest("test/op", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}

		mu.Lock()
		seen++
		order := seen
		mu.Unlock()
		// The first request to arrive answers last -- reversed responses.
		if order == 1 {
			time.Sleep(300 * time.Millisecond)
		}

		return map[string]any{"echo": p.N}, nil
	})

	const n = 8

	type result struct {
		n   int
		err error
	}

	errCh := make(chan result, n)
	for i := 1; i <= n; i++ {
		go func(n int) {
			raw, err := c1.Call(context.Background(), "test/op", map[string]any{"n": n})
			if err != nil {
				errCh <- result{n, err}
				return
			}

			var resp struct {
				Echo int `json:"echo"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				errCh <- result{n, err}
				return
			}

			if resp.Echo != n {
				errCh <- result{n, fmt.Errorf("call %d got echo %d", n, resp.Echo)}
				return
			}

			errCh <- result{n, nil}
		}(i)
	}

	for range n {
		if r := <-errCh; r.err != nil {
			t.Fatalf("concurrent call %d: %v", r.n, r.err)
		}
	}
}
