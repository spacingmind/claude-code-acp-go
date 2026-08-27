package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// acpTestHarness is the fake ACP client side of the two-layer test setup:
// it pipes NDJSON JSON-RPC to the Agent (our library) and captures
// everything the Agent sends back. Underneath, the Agent spawns real
// claudecode.Clients pointed at this test binary running as the fake
// claude CLI (see fakecli_test.go).
type acpTestHarness struct {
	t     *testing.T
	conn  *Connection // harness-side connection (speaks to the agent)
	agent *Agent

	clientReader *io.PipeReader // agent reads from here
	clientWriter *io.PipeWriter // agent writes to here

	mu       sync.Mutex
	updates  []SessionNotification
	permReqs []permCapture
}

type permCapture struct {
	ID  RequestID
	Req RequestPermissionRequest
}

func newACPHarness(t *testing.T, cliOpts []claudecode.Option) *acpTestHarness {
	// cliOpts may be nil: a harness that only exercises protocol-level
	// error paths never spawns a fake CLI.
	t.Helper()

	// Agent's stdin: we write requests into clientWriter, agent reads from
	// clientReader. Agent's stdout: agent writes into agentWriter, we read
	// from agentReader.
	clientReader, clientWriter := io.Pipe()
	agentReader, agentWriter := io.Pipe()

	h := &acpTestHarness{
		t:            t,
		clientReader: clientReader,
		clientWriter: clientWriter,
	}

	agentConn := NewConnection(clientReader, agentWriter)
	agent := NewAgent(agentConn, cliOpts...)
	h.agent = agent

	h.conn = NewConnection(agentReader, clientWriter)
	h.conn.RegisterNotification(MethodSessionUpdate, func(_ context.Context, params json.RawMessage) {
		var n SessionNotification
		if err := json.Unmarshal(params, &n); err != nil {
			t.Errorf("harness: bad session/update: %v", err)
			return
		}

		h.mu.Lock()
		h.updates = append(h.updates, n)
		h.mu.Unlock()
	})

	t.Cleanup(func() {
		// Closing the agent's stdin ends its read loop, which closes all
		// session Clients (fake CLI subprocesses) via Agent.closeAll.
		// Wait for that teardown to finish so no claude subprocess
		// outlives the test binary.
		_ = clientWriter.Close()

		select {
		case <-agent.Closed():
		case <-time.After(10 * time.Second):
			t.Errorf("agent did not close its sessions within 10s of connection teardown")
		}

		_ = agentWriter.Close()
		_ = clientReader.Close()
		_ = agentReader.Close()
	})

	return h
}

// call sends a JSON-RPC request with the given raw id and waits for the
// correlated response.
func (h *acpTestHarness) call(id any, method string, params any) (*Response, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	var rid RequestID

	switch v := id.(type) {
	case int:
		rid = RequestID{kind: idNumber, num: int64(v)}
	case string:
		rid = RequestID{kind: idString, str: v}
	}

	respCh := make(chan Response, 1)

	h.conn.mu.Lock()
	h.conn.pending[rid] = respCh
	h.conn.mu.Unlock()

	if err := h.conn.writeLine(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      RequestID       `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{"2.0", rid, method, raw}); err != nil {
		return nil, err
	}

	select {
	case resp := <-respCh:
		return &resp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("timed out waiting for response to %s", method)
	}
}

// notify sends a JSON-RPC notification.
func (h *acpTestHarness) notify(method string, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		h.t.Fatalf("harness notify marshal: %v", err)
	}

	if err := h.conn.writeLine(struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{"2.0", method, raw}); err != nil {
		h.t.Fatalf("harness notify write: %v", err)
	}
}

// writeRaw writes a pre-baked JSON line straight to the agent.
func (h *acpTestHarness) writeRaw(line string) {
	if _, err := fmt.Fprintf(h.clientWriter, "%s\n", line); err != nil {
		h.t.Fatalf("harness writeRaw: %v", err)
	}
}

// updatesSnapshot returns the updates captured so far.
func (h *acpTestHarness) updatesSnapshot() []SessionNotification {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]SessionNotification(nil), h.updates...)
}

// waitForUpdates polls until at least n session/updates have arrived for
// the session (or times out).
func (h *acpTestHarness) waitForUpdates(sessionID string, n int) []SessionNotification {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var got []SessionNotification

		for _, u := range h.updatesSnapshot() {
			if u.SessionID == sessionID {
				got = append(got, u)
			}
		}

		if len(got) >= n {
			return got
		}

		time.Sleep(10 * time.Millisecond)
	}

	h.t.Fatalf("timed out waiting for %d updates for session %s", n, sessionID)

	return nil
}

// servePermissions registers the permission-responder: every inbound
// session/request_permission is recorded and answered by the supplied
// function (which may block -- it runs on its own goroutine).
func (h *acpTestHarness) servePermissions(answer func(req RequestPermissionRequest) RequestPermissionResponse) {
	h.conn.RegisterRequest(MethodRequestPermission, func(_ context.Context, params json.RawMessage) (any, error) {
		var req RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}

		h.mu.Lock()
		h.permReqs = append(h.permReqs, permCapture{Req: req})
		h.mu.Unlock()

		return answer(req), nil
	})
}

// permCount returns how many permission requests have been seen.
func (h *acpTestHarness) permCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.permReqs)
}
