package acp

import (
	"encoding/json"
	"strings"
	"sync"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// toolCallState tracks per-session tool-call metadata (title/kind chosen
// when the tool_use block was first seen) so later updates and permission
// requests reuse them. It is mutated from the prompt-draining goroutine
// (remember, during message translation) and read from the SDK's inbound
// can_use_tool handler goroutine (title/kind, via the permission policy),
// so every access takes the lock.
type toolCallState struct {
	mu     sync.RWMutex
	titles map[string]string
	kinds  map[string]ToolKind
}

func newToolCallState() *toolCallState {
	return &toolCallState{titles: map[string]string{}, kinds: map[string]ToolKind{}}
}

func (s *toolCallState) remember(id, title string, kind ToolKind) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.titles[id] = title
	s.kinds[id] = kind
}

func (s *toolCallState) title(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.titles[id]
}

// mapToolKind derives an ACP ToolKind from a Claude Code tool name.
func mapToolKind(name string) ToolKind {
	switch name {
	case "Read":
		return ToolKindRead
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		return ToolKindEdit
	case "Bash", "BashOutput":
		return ToolKindExecute
	case "Grep", "Glob":
		return ToolKindSearch
	case "WebFetch", "WebSearch":
		return ToolKindFetch
	default:
		return ToolKindOther
	}
}

// toolTitle builds a short human-readable title like "Read foo.go" or
// "Bash: go test ./...".
func toolTitle(name string, input map[string]any) string {
	summary := summarizeToolInput(name, input)
	if summary == "" {
		return name
	}

	switch name {
	case "Bash", "BashOutput":
		return name + ": " + summary
	default:
		return name + " " + summary
	}
}

//nolint:gocyclo  // flat per-tool-name summary extraction
func summarizeToolInput(name string, input map[string]any) string {
	getString := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := input[k].(string); ok && v != "" {
				return v
			}
		}

		return ""
	}

	switch name {
	case "Read", "Edit", "Write", "NotebookEdit", "Glob", "WebFetch":
		if s := getString("file_path", "filePath", "notebook_path", "path", "url", "pattern"); s != "" {
			return shortPath(s)
		}
	case "MultiEdit":
		if s := getString("file_path", "filePath"); s != "" {
			return shortPath(s)
		}
	case "Bash", "BashOutput":
		if s := getString("command", "cmd"); s != "" {
			if len(s) > 60 {
				s = s[:57] + "..."
			}

			return s
		}
	case "Grep":
		if s := getString("pattern"); s != "" {
			if len(s) > 40 {
				s = s[:37] + "..."
			}

			return s
		}
	case "WebSearch":
		if s := getString("query"); s != "" {
			if len(s) > 60 {
				s = s[:57] + "..."
			}

			return s
		}
	case "Task":
		if s := getString("description", "prompt"); s != "" {
			if len(s) > 60 {
				s = s[:57] + "..."
			}

			return s
		}
	}

	return ""
}

func shortPath(p string) string {
	if len(p) > 40 {
		parts := strings.Split(p, "/")
		if len(parts) > 3 {
			return ".../" + strings.Join(parts[len(parts)-3:], "/")
		}

		if len(p) > 60 {
			return p[:57] + "..."
		}
	}

	return p
}

// translateMessage converts one SDK message into zero or more ACP
// session/update values. Messages with no ACP equivalent (system, hook,
// stream, rate-limit events) produce nothing.
func translateMessage(msg claudecode.Message, tools *toolCallState) []SessionUpdate {
	switch m := msg.(type) {
	case claudecode.AssistantMessage:
		return translateBlocks(m.Content, tools)
	case claudecode.UserMessage:
		return translateBlocks(m.Content, tools)
	case claudecode.ResultMessage:
		return nil
	default:
		return nil
	}
}

func translateBlocks(blocks []claudecode.ContentBlock, tools *toolCallState) []SessionUpdate {
	var out []SessionUpdate

	for _, b := range blocks {
		switch blk := b.(type) {
		case claudecode.TextBlock:
			out = append(out, SessionUpdate{AgentMessageChunk: &ContentChunk{
				Content: ContentBlock{Text: &TextContent{Type: contentTypeText, Text: blk.Text}},
			}})
		case claudecode.ThinkingBlock:
			out = append(out, SessionUpdate{AgentThoughtChunk: &ContentChunk{
				Content: ContentBlock{Text: &TextContent{Type: contentTypeText, Text: blk.Thinking}},
			}})
		case claudecode.ToolUseBlock:
			kind := mapToolKind(blk.Name)
			title := toolTitle(blk.Name, blk.Input)
			tools.remember(blk.ID, title, kind)
			out = append(out,
				SessionUpdate{ToolCall: &ToolCall{
					ToolCallID: blk.ID,
					Title:      title,
					Kind:       kind,
					Status:     ToolCallStatusPending,
				}},
				SessionUpdate{ToolCallUpdate: &ToolCallUpdate{
					ToolCallID: blk.ID,
					Status:     &inProgressStatus,
				}})
		case claudecode.ToolResultBlock:
			out = append(out, SessionUpdate{ToolCallUpdate: &ToolCallUpdate{
				ToolCallID: blk.ToolUseID,
				Status:     toolResultStatusPtr(blk.IsError),
				Content:    toolResultContent(blk.Content),
			}})
		}
	}

	return out
}

//go:fix inline
var inProgressStatus = ToolCallStatusInProgress

func toolResultStatusPtr(isError bool) *ToolCallStatus {
	if isError {
		failed := ToolCallStatusFailed
		return &failed
	}

	completed := ToolCallStatusCompleted

	return &completed
}

// toolResultContent flattens the SDK's raw tool-result content (either a
// plain string or an array of blocks) into ACP tool call content. Non-text
// blocks are skipped; ACP has no slot for them in this phase.
func toolResultContent(raw json.RawMessage) []ToolCallContent {
	if len(raw) == 0 {
		return nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []ToolCallContent{{Text: &TextContent{Type: contentTypeText, Text: s}}}
	}

	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var out []ToolCallContent

		for _, b := range arr {
			if b.Type == contentTypeText {
				out = append(out, ToolCallContent{Text: &TextContent{Type: contentTypeText, Text: b.Text}})
			}
		}

		return out
	}

	return []ToolCallContent{{Text: &TextContent{Type: contentTypeText, Text: string(raw)}}}
}
