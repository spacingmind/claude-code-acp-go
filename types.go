package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// RequestID is a JSON-RPC request ID: null, number, or string. It
// round-trips the exact value (including string IDs and numbers outside
// int64 range) back on responses. It is a comparable value type so it can
// key the transport's pending-response map.
type RequestID struct {
	kind   idKind
	num    int64
	numRaw string // exact JSON text for numbers that don't fit int64
	str    string
}

type idKind int

const (
	idNull idKind = iota
	idNumber
	idString
)

// MarshalJSON writes the exact wire form: null, the number, or the string.
func (id RequestID) MarshalJSON() ([]byte, error) {
	switch id.kind {
	case idNull:
		return []byte("null"), nil
	case idNumber:
		if id.numRaw != "" {
			return []byte(id.numRaw), nil
		}

		return json.Marshal(id.num)
	case idString:
		return json.Marshal(id.str)
	}

	return []byte("null"), nil
}

// UnmarshalJSON accepts null, numbers (keeping exact text beyond int64), and strings.
func (id *RequestID) UnmarshalJSON(data []byte) error {
	*id = RequestID{}

	if string(data) == "null" {
		return nil
	}

	s := strings.TrimSpace(string(data))
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}

		*id = RequestID{kind: idString, str: str}

		return nil
	}

	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}

	if i, err := n.Int64(); err == nil {
		*id = RequestID{kind: idNumber, num: i}
		return nil
	}

	*id = RequestID{kind: idNumber, numRaw: n.String()}

	return nil
}

// GoString renders the ID as its JSON form for debug output.
func (id RequestID) GoString() string {
	b, _ := id.MarshalJSON()
	return string(b)
}

// --- JSON-RPC envelopes ---

// Request is an inbound or outbound JSON-RPC request envelope.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *RequestID      `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC response envelope, carrying either a result or an error.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *RequestID      `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RequestError   `json:"error,omitempty"`
}

// RequestError is a JSON-RPC error object, also used as the Go-side handler error for wire-mappable failures.
type RequestError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RequestError) Error() string { return e.Message }

// JSON-RPC and ACP error codes used in error responses.
const (
	CodeParseError       = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodeInternalError    = -32603
	CodeResourceNotFound = -32002
)

// --- initialize ---

// Implementation names a protocol participant (client or agent) and its version.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities declares what the ACP client supports.
type ClientCapabilities struct {
	FS       json.RawMessage `json:"fs,omitempty"`
	Terminal json.RawMessage `json:"terminal,omitempty"`
}

// InitializeRequest is the initialize handshake request.
type InitializeRequest struct {
	ProtocolVersion    int                 `json:"protocolVersion"`
	ClientCapabilities *ClientCapabilities `json:"clientCapabilities"`
	Client             *Implementation     `json:"client"`
}

// PromptCapabilities declares which prompt content types the agent accepts.
type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

// McpCapabilities declares which MCP server types the agent accepts in session/new.
type McpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// SessionCapabilities declares optional session methods (list, delete, resume, close) the agent supports.
type SessionCapabilities struct {
	List   bool `json:"list,omitempty"`
	Delete bool `json:"delete,omitempty"`
	Resume bool `json:"resume,omitempty"`
	// Close is an object in the schema; omitted until supported.
}

// AgentCapabilities declares the agent feature set returned from initialize.
type AgentCapabilities struct {
	LoadSession         bool                `json:"loadSession"`
	PromptCapabilities  PromptCapabilities  `json:"promptCapabilities"`
	McpCapabilities     McpCapabilities     `json:"mcpCapabilities"`
	SessionCapabilities SessionCapabilities `json:"sessionCapabilities"`
}

// InitializeResponse is the agent reply to initialize.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []json.RawMessage `json:"authMethods"`
}

// --- session/new ---

// McpServer is a tagged union over the ACP MCP server variants; exactly one pointer field is set.
type McpServer struct {
	Stdio *McpServerStdio
	HTTP  *McpServerHTTP
	SSE   *McpServerSSE
}

// McpServerStdio is a stdio-launched MCP server (the only variant supported this phase).
type McpServerStdio struct {
	Type    string   `json:"type,omitempty"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
}

// McpServerHTTP is a streamable-HTTP MCP server (advertised but rejected this phase).
type McpServerHTTP struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// McpServerSSE is an SSE MCP server (advertised but rejected this phase).
type McpServerSSE struct {
	Type string `json:"type"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// MarshalJSON writes the variant object with the type discriminator forced from the Go-side variant.
func (s McpServer) MarshalJSON() ([]byte, error) {
	var (
		variantType string
		value       any
	)

	switch {
	case s.Stdio != nil:
		variantType, value = "stdio", s.Stdio
	case s.HTTP != nil:
		variantType, value = "http", s.HTTP
	case s.SSE != nil:
		variantType, value = "sse", s.SSE
	default:
		return nil, errors.New("acp: empty mcp server")
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	// Force the discriminator: the Go-side variant is authoritative even
	// if the inner struct's Type field was left unset.
	m["type"] = json.RawMessage(`"` + variantType + `"`)

	return json.Marshal(m)
}

// UnmarshalJSON discriminates on type: missing or "stdio" is stdio; "http" and "sse" decode into their variants.
func (s *McpServer) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type string `json:"type"`
	}

	_ = json.Unmarshal(data, &tag)
	switch tag.Type {
	case "", "stdio":
		var v McpServerStdio
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}

		s.Stdio, s.HTTP, s.SSE = &v, nil, nil
	case "http":
		var v McpServerHTTP
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}

		s.Stdio, s.HTTP, s.SSE = nil, &v, nil
	case "sse":
		var v McpServerSSE
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}

		s.Stdio, s.HTTP, s.SSE = nil, nil, &v
	default:
		return fmt.Errorf("acp: unknown mcp server type %q", tag.Type)
	}

	return nil
}

// NewSessionRequest opens a session rooted at CWD with the given MCP servers.
type NewSessionRequest struct {
	CWD        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// NewSessionResponse carries the newly created opaque session ID.
type NewSessionResponse struct {
	SessionID string            `json:"sessionId"`
	Modes     *SessionModeState `json:"modes,omitempty"`
}

// --- session modes ---

// SessionMode is one selectable agent permission mode.
type SessionMode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionModeState reports a session's current mode and the fixed set of modes it accepts.
type SessionModeState struct {
	CurrentModeID  string        `json:"currentModeId"`
	AvailableModes []SessionMode `json:"availableModes"`
}

// --- session/load, session/resume ---

// LoadSessionRequest loads an existing local session, replaying its history.
type LoadSessionRequest struct {
	SessionID  string      `json:"sessionId"`
	CWD        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// LoadSessionResponse concludes session/load.
type LoadSessionResponse struct {
	Modes *SessionModeState `json:"modes,omitempty"`
}

// ResumeSessionRequest resumes an existing local session without history replay.
type ResumeSessionRequest struct {
	SessionID      string      `json:"sessionId"`
	CWD            string      `json:"cwd"`
	AdditionalDirs []string    `json:"additionalDirectories,omitempty"`
	McpServers     []McpServer `json:"mcpServers"`
}

// ResumeSessionResponse concludes session/resume.
type ResumeSessionResponse struct {
	Modes *SessionModeState `json:"modes,omitempty"`
}

// --- session/list ---

// ListSessionsRequest lists stored sessions, optionally scoped to a project directory.
type ListSessionsRequest struct {
	CWD    string `json:"cwd,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// SessionInfo describes one stored session.
type SessionInfo struct {
	SessionID string  `json:"sessionId"`
	CWD       string  `json:"cwd"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

// ListSessionsResponse concludes session/list. This phase never paginates,
// so NextCursor is always nil.
type ListSessionsResponse struct {
	Sessions   []SessionInfo `json:"sessions"`
	NextCursor *string       `json:"nextCursor,omitempty"`
}

// --- session/set_mode ---

// SetSessionModeRequest switches a session's permission mode.
type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetSessionModeResponse concludes session/set_mode; always empty.
type SetSessionModeResponse struct{}

// --- session/prompt ---

// contentTypeText is the "type" discriminator value for TextContent, the
// only ContentBlock variant this phase supports.
const contentTypeText = "text"

// ContentBlock is a tagged union over ACP content blocks; only the text variant exists this phase.
type ContentBlock struct {
	Text *TextContent
}

// TextContent is the "text" content block variant.
type TextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// MarshalJSON writes the set variant's object.
func (c ContentBlock) MarshalJSON() ([]byte, error) {
	switch {
	case c.Text != nil:
		return json.Marshal(c.Text)
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON accepts only "text" blocks this phase.
func (c *ContentBlock) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	switch tag.Type {
	case contentTypeText:
		var v TextContent
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}

		c.Text = &v

		return nil
	default:
		return fmt.Errorf("acp: unsupported content block type %q", tag.Type)
	}
}

// PromptRequest sends one prompt turn to a session.
type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// StopReason explains why a prompt turn ended.
type StopReason string

// StopReason values from the ACP schema.
const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonCancelled       StopReason = "cancelled"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
)

// PromptResponse concludes a session/prompt call.
type PromptResponse struct {
	StopReason StopReason `json:"stopReason"`
}

// --- session/cancel ---

// CancelNotification asks the agent to interrupt a session's in-flight turn.
type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// --- session/update ---

// SessionNotification streams one update about a session to the client.
type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// SessionUpdate is a tagged union keyed on the sessionUpdate discriminator; exactly one pointer field is set.
type SessionUpdate struct {
	AgentMessageChunk *ContentChunk
	AgentThoughtChunk *ContentChunk
	UserMessageChunk  *ContentChunk
	ToolCall          *ToolCall
	ToolCallUpdate    *ToolCallUpdate
	CurrentModeUpdate *CurrentModeUpdate
}

// CurrentModeUpdate announces the session's current mode (e.g. after session/set_mode).
type CurrentModeUpdate struct {
	CurrentModeID string `json:"currentModeId"`
}

// ContentChunk wraps one content block streamed as a message or thought chunk.
type ContentChunk struct {
	Content ContentBlock `json:"content"`
}

// ToolKind classifies a tool call for the client UI.
type ToolKind string

// ToolKind values from the ACP schema.
const (
	ToolKindRead    ToolKind = "read"
	ToolKindEdit    ToolKind = "edit"
	ToolKindExecute ToolKind = "execute"
	ToolKindSearch  ToolKind = "search"
	ToolKindFetch   ToolKind = "fetch"
	ToolKindOther   ToolKind = "other"
)

// ToolCallStatus is a tool call lifecycle state.
type ToolCallStatus string

// ToolCallStatus values from the ACP schema.
const (
	ToolCallStatusPending    ToolCallStatus = "pending"
	ToolCallStatusInProgress ToolCallStatus = "in_progress"
	ToolCallStatusCompleted  ToolCallStatus = "completed"
	ToolCallStatusFailed     ToolCallStatus = "failed"
)

// ToolCall announces a tool invocation and its lifecycle state.
type ToolCall struct {
	ToolCallID string            `json:"toolCallId"`
	Title      string            `json:"title"`
	Kind       ToolKind          `json:"kind"`
	Status     ToolCallStatus    `json:"status"`
	Content    []ToolCallContent `json:"content,omitempty"`
	RawInput   json.RawMessage   `json:"rawInput,omitempty"`
	Locations  json.RawMessage   `json:"locations,omitempty"`
}

// ToolCallUpdate evolves a previously announced tool call (status, content, input).
type ToolCallUpdate struct {
	ToolCallID string            `json:"toolCallId"`
	Title      string            `json:"title,omitempty"`
	Kind       ToolKind          `json:"kind,omitempty"`
	Status     *ToolCallStatus   `json:"status,omitempty"`
	Content    []ToolCallContent `json:"content,omitempty"`
	RawInput   json.RawMessage   `json:"rawInput,omitempty"`
	Locations  json.RawMessage   `json:"locations,omitempty"`
}

// ToolCallContent is a tagged union over tool call content blocks; text only this phase.
type ToolCallContent struct {
	Text *TextContent
	// Future variants (image, resource-link, etc.) would extend here.
}

// MarshalJSON writes the set variant's object.
func (c ToolCallContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil {
		return json.Marshal(c.Text)
	}

	return []byte("null"), nil
}

// UnmarshalJSON accepts only "text" blocks this phase.
func (c *ToolCallContent) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}

	switch tag.Type {
	case contentTypeText:
		var v TextContent
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}

		c.Text = &v

		return nil
	default:
		return fmt.Errorf("acp: unsupported tool call content type %q", tag.Type)
	}
}

// MarshalJSON flattens the set variant's fields plus the sessionUpdate discriminator into one object.
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	var (
		variant string
		value   any
	)

	switch {
	case u.AgentMessageChunk != nil:
		variant, value = "agent_message_chunk", u.AgentMessageChunk
	case u.AgentThoughtChunk != nil:
		variant, value = "agent_thought_chunk", u.AgentThoughtChunk
	case u.UserMessageChunk != nil:
		variant, value = "user_message_chunk", u.UserMessageChunk
	case u.ToolCall != nil:
		variant, value = "tool_call", u.ToolCall
	case u.ToolCallUpdate != nil:
		variant, value = "tool_call_update", u.ToolCallUpdate
	case u.CurrentModeUpdate != nil:
		variant, value = "current_mode_update", u.CurrentModeUpdate
	default:
		return nil, errors.New("acp: empty session update")
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}

	m["sessionUpdate"] = json.RawMessage(`"` + variant + `"`)

	return json.Marshal(m)
}

// UnmarshalJSON decodes the flattened variant object back into the
// matching pointer field, discriminating on sessionUpdate.
//
//nolint:gocyclo  // flat one-case-per-variant tagged-union decoding
func (u *SessionUpdate) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	var variant string
	if raw, ok := m["sessionUpdate"]; ok {
		if err := json.Unmarshal(raw, &variant); err != nil {
			return err
		}
	} else {
		return errors.New("acp: session update missing sessionUpdate field")
	}

	delete(m, "sessionUpdate")

	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}

	*u = SessionUpdate{}

	switch variant {
	case "agent_message_chunk":
		var v ContentChunk
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.AgentMessageChunk = &v
	case "agent_thought_chunk":
		var v ContentChunk
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.AgentThoughtChunk = &v
	case "user_message_chunk":
		var v ContentChunk
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.UserMessageChunk = &v
	case "tool_call":
		var v ToolCall
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.ToolCall = &v
	case "tool_call_update":
		var v ToolCallUpdate
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.ToolCallUpdate = &v
	case "current_mode_update":
		var v CurrentModeUpdate
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}

		u.CurrentModeUpdate = &v
	default:
		return fmt.Errorf("acp: unknown sessionUpdate variant %q", variant)
	}

	return nil
}

// --- session/request_permission ---

// RequestPermissionRequest asks the client to decide a pending tool call.
type RequestPermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOption is one selectable choice in a permission request.
type PermissionOption struct {
	OptionID string               `json:"optionId"`
	Name     string               `json:"name"`
	Kind     PermissionOptionKind `json:"kind"`
}

// PermissionOptionKind classifies what choosing an option means.
type PermissionOptionKind string

// PermissionOptionKind values used in this phase's binary allow/deny options.
const (
	PermissionOptionKindAllowOnce  PermissionOptionKind = "allow_once"
	PermissionOptionKindRejectOnce PermissionOptionKind = "reject_once"
)

// RequestPermissionResponse carries the client's permission decision.
type RequestPermissionResponse struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// RequestPermissionOutcome is a tagged union: either a selected option or cancelled.
type RequestPermissionOutcome struct {
	Selected  *SelectedPermissionOutcome
	Cancelled bool
}

// SelectedPermissionOutcome names the permission option the client chose.
type SelectedPermissionOutcome struct {
	OptionID string `json:"optionId"`
}

// MarshalJSON writes either the selected option object or {"cancelled":true}.
func (o RequestPermissionOutcome) MarshalJSON() ([]byte, error) {
	if o.Selected != nil {
		return json.Marshal(o.Selected)
	}

	return json.Marshal(map[string]any{"cancelled": true})
}

// UnmarshalJSON decodes either a selected option or the cancelled marker.
func (o *RequestPermissionOutcome) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	if _, ok := m["cancelled"]; ok {
		o.Cancelled = true
		return nil
	}

	var s SelectedPermissionOutcome
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}

	o.Selected = &s

	return nil
}
