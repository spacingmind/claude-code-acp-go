// Package acp implements an ACP (Agent Client Protocol) agent that wraps
// Claude Code via the claude-agent-sdk-go client, speaking JSON-RPC 2.0
// over NDJSON (stdin/stdout in production).
package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// ACP method names handled or emitted by the agent.
const (
	MethodInitialize        = "initialize"
	MethodNewSession        = "session/new"
	MethodPrompt            = "session/prompt"
	MethodCancel            = "session/cancel"
	MethodSessionUpdate     = "session/update"
	MethodRequestPermission = "session/request_permission"
)

// session is one ACP session backed by one SDK Client.
type session struct {
	client *claudecode.Client
	tools  *toolCallState

	mu         sync.Mutex
	cancelled  bool            // current turn cancelled via session/cancel (AC 13)
	turnCtx    context.Context //nolint:containedctx  // per-turn cancel basis, cancelled by session/cancel
	turnCancel context.CancelFunc
}

// beginTurn resets per-turn state and installs the turn's context; the
// turn context is what pending permission round trips are scoped to, so
// session/cancel unblocks them too (AC 12).
func (s *session) beginTurn(ctx context.Context, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelled = false
	s.turnCtx = ctx
	s.turnCancel = cancel
}

// endTurn clears the turn's context (turn finished normally).
func (s *session) endTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.turnCtx = nil
	s.turnCancel = nil
}

// currentTurnCtx returns the live turn's context (Background if no turn
// is in flight).
func (s *session) currentTurnCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.turnCtx != nil {
		return s.turnCtx
	}

	return context.Background()
}

func (s *session) wasCancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.cancelled
}

func (s *session) setCancelled(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cancelled = v
}

// cancelTurn cancels the current turn's context, unblocking anything
// scoped to it (e.g. a pending session/request_permission round trip).
func (s *session) cancelTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.turnCancel != nil {
		s.turnCancel()
	}
}

// Agent is the ACP agent: it receives JSON-RPC requests/notifications from
// an ACP client over a Connection and drives Claude Code via the SDK.
type Agent struct {
	conn *Connection

	clientOpts []claudecode.Option // e.g. WithCLIPath for tests

	mu       sync.Mutex
	sessions map[string]*session

	closed chan struct{} // closed once all session Clients are closed
}

// NewAgent wires up the ACP method handlers on conn. When conn ends
// (read side closed), every session's underlying SDK Client is closed so
// no claude subprocess outlives the connection.
func NewAgent(conn *Connection, clientOpts ...claudecode.Option) *Agent {
	a := &Agent{
		conn:       conn,
		clientOpts: clientOpts,
		sessions:   make(map[string]*session),
		closed:     make(chan struct{}),
	}
	conn.RegisterRequest(MethodInitialize, a.handleInitialize)
	conn.RegisterRequest(MethodNewSession, a.handleNewSession)
	conn.RegisterRequest(MethodPrompt, a.handlePrompt)

	conn.RegisterNotification(MethodCancel, a.handleCancel)
	go func() {
		defer close(a.closed)

		<-conn.Done()
		a.closeAll()
	}()

	return a
}

// Closed returns a channel closed once every session's underlying Client
// (and thus its claude subprocess) has been torn down after the
// connection ended.
func (a *Agent) Closed() <-chan struct{} { return a.closed }

func (a *Agent) closeAll() {
	a.mu.Lock()

	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()

	for _, s := range sessions {
		if err := s.client.Close(); err != nil {
			log.Printf("acp: close session client: %v", err)
		}
	}
}

func (a *Agent) handleInitialize(_ context.Context, _ json.RawMessage) (any, error) {
	return InitializeResponse{
		ProtocolVersion: 1,
		AgentCapabilities: AgentCapabilities{
			LoadSession:         false,
			PromptCapabilities:  PromptCapabilities{Image: false, Audio: false, EmbeddedContext: false},
			McpCapabilities:     McpCapabilities{HTTP: false, SSE: false},
			SessionCapabilities: SessionCapabilities{},
		},
		AuthMethods: []json.RawMessage{},
	}, nil
}

func (a *Agent) handleNewSession(_ context.Context, params json.RawMessage) (any, error) {
	var req NewSessionRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &RequestError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid session/new params: %v", err)}
	}

	if req.CWD == "" {
		return nil, &RequestError{Code: CodeInvalidParams, Message: "session/new: cwd is required"}
	}

	var opts []claudecode.Option

	opts = append(opts, a.clientOpts...)

	if len(req.McpServers) > 0 {
		config, err := mcpConfigJSON(req.McpServers)
		if err != nil {
			return nil, err
		}

		opts = append(opts, claudecode.WithMCPConfig(config))
	}

	sessionID := newSessionID()
	sess := &session{tools: newToolCallState()}

	opts = append(opts, claudecode.WithPermissionPolicy(&acpPermissionPolicy{
		agent: a, sessionID: sessionID, session: sess,
	}))

	client, err := claudecode.New(req.CWD, opts...)
	if err != nil {
		return nil, &RequestError{Code: CodeInternalError, Message: fmt.Sprintf("failed to start claude code: %v", err)}
	}

	sess.client = client

	a.mu.Lock()
	a.sessions[sessionID] = sess
	a.mu.Unlock()

	return NewSessionResponse{SessionID: sessionID}, nil
}

// mcpConfigJSON builds the --mcp-config JSON blob from the ACP server
// list. Only stdio servers are supported (AC 6); http/sse are rejected.
func mcpConfigJSON(servers []McpServer) (string, error) {
	serversMap := map[string]any{}

	for i, s := range servers {
		switch {
		case s.Stdio != nil:
			entry := map[string]any{
				"type":    "stdio",
				"command": s.Stdio.Command,
			}
			if len(s.Stdio.Args) > 0 {
				entry["args"] = s.Stdio.Args
			}

			if len(s.Stdio.Env) > 0 {
				entry["env"] = envMap(s.Stdio.Env)
			}

			serversMap[s.Stdio.Name] = entry
		case s.HTTP != nil:
			return "", &RequestError{Code: CodeInvalidParams, Message: fmt.Sprintf("mcp server %d (%q): http servers are not supported", i, s.HTTP.Name)}
		case s.SSE != nil:
			return "", &RequestError{Code: CodeInvalidParams, Message: fmt.Sprintf("mcp server %d (%q): sse servers are not supported", i, s.SSE.Name)}
		default:
			return "", &RequestError{Code: CodeInvalidParams, Message: fmt.Sprintf("mcp server %d: no supported variant", i)}
		}
	}

	data, err := json.Marshal(map[string]any{"mcpServers": serversMap})
	if err != nil {
		return "", &RequestError{Code: CodeInternalError, Message: err.Error()}
	}

	return string(data), nil
}

func envMap(kv []string) map[string]string {
	m := map[string]string{}

	for _, e := range kv {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}

	return m
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	// RFC 4122 version 4 shape; uniqueness is all ACP requires.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])

	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

func (a *Agent) lookupSession(sessionID string) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	s, ok := a.sessions[sessionID]
	if !ok {
		return nil, &RequestError{Code: CodeResourceNotFound, Message: "session not found: " + sessionID}
	}

	return s, nil
}

//nolint:gocyclo  // linear request pipeline: decode, validate, run, translate, respond
func (a *Agent) handlePrompt(ctx context.Context, params json.RawMessage) (any, error) {
	var req PromptRequest
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &RequestError{Code: CodeInvalidParams, Message: fmt.Sprintf("invalid session/prompt params: %v", err)}
	}

	var texts []string

	for _, b := range req.Prompt {
		if b.Text == nil {
			return nil, &RequestError{Code: CodeInvalidParams, Message: "session/prompt: only text content blocks are supported"}
		}

		texts = append(texts, b.Text.Text)
	}

	if len(texts) == 0 {
		return nil, &RequestError{Code: CodeInvalidParams, Message: "session/prompt: prompt must contain at least one text block"}
	}

	promptText := strings.Join(texts, "\n")

	sess, err := a.lookupSession(req.SessionID)
	if err != nil {
		return nil, err
	}

	turnContext, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()

	sess.beginTurn(turnContext, cancelTurn) // fresh turn (AC 13)

	if err := sess.client.QueryWithSession(ctx, promptText, req.SessionID); err != nil {
		if sess.wasCancelled() || errors.Is(err, context.Canceled) {
			return PromptResponse{StopReason: StopReasonCancelled}, nil
		}

		return nil, &RequestError{Code: CodeInternalError, Message: fmt.Sprintf("query failed: %v", err)}
	}

	var result *claudecode.ResultMessage

	defer sess.endTurn()

	for msg := range sess.client.ReceiveResponse(ctx) {
		if r, ok := msg.(claudecode.ResultMessage); ok {
			rr := r
			result = &rr

			continue
		}

		for _, u := range translateMessage(msg, sess.tools) {
			if err := a.conn.Notify(MethodSessionUpdate, SessionNotification{SessionID: req.SessionID, Update: u}); err != nil {
				log.Printf("acp: session/update notify: %v", err)
			}
		}
	}

	if result == nil {
		if sess.wasCancelled() {
			return PromptResponse{StopReason: StopReasonCancelled}, nil
		}

		return PromptResponse{StopReason: StopReasonEndTurn}, nil
	}

	return PromptResponse{StopReason: mapStopReason(result, sess.wasCancelled())}, nil
}

// mapStopReason maps the SDK's terminal ResultMessage to an ACP StopReason
// (AC 11). Chosen mapping: cancelled flag wins; else a permission-denial
// driven stop maps to "refusal"; a max-turns stop to "max_turn_requests";
// errors are surfaced as a preceding agent_message_chunk (see handlePrompt
// callers) with "end_turn" as fallback.
func mapStopReason(r *claudecode.ResultMessage, cancelled bool) StopReason {
	if cancelled {
		return StopReasonCancelled
	}

	if r.StopReason == "refusal" || len(r.PermissionDenials) > 0 {
		return StopReasonRefusal
	}

	if r.TerminalReason == "max_turn_requests" || r.StopReason == "max_turn_requests" {
		return StopReasonMaxTurnRequests
	}

	return StopReasonEndTurn
}

func (a *Agent) handleCancel(ctx context.Context, params json.RawMessage) {
	var n CancelNotification
	if err := json.Unmarshal(params, &n); err != nil {
		log.Printf("acp: invalid session/cancel params: %v", err)
		return
	}

	sess, err := a.lookupSession(n.SessionID)
	if err != nil {
		return
	}

	sess.setCancelled(true)
	sess.cancelTurn()

	if err := sess.client.Interrupt(ctx); err != nil {
		log.Printf("acp: interrupt session %s: %v", n.SessionID, err)
	}
}

// Sessions returns the IDs of all live sessions (used by tests and the
// permission policy to check liveness).
func (a *Agent) Sessions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()

	return slices.Collect(maps.Keys(a.sessions))
}
