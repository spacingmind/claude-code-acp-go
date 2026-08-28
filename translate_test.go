package acp

import (
	"encoding/json"
	"strings"
	"testing"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input map[string]any
		want  string
	}{
		{"read file_path", "Read", map[string]any{"file_path": "/a/b/c.go"}, "/a/b/c.go"},
		{"read filePath fallback", "Read", map[string]any{"filePath": "/x.go"}, "/x.go"},
		{"multiedit", "MultiEdit", map[string]any{"file_path": "/a/b.go"}, "/a/b.go"},
		{"bash short", "Bash", map[string]any{"command": "go test ./..."}, "go test ./..."},
		{"bash long truncates", "Bash", map[string]any{"command": strings.Repeat("x", 100)}, strings.Repeat("x", 57) + "..."},
		{"bash cmd fallback", "BashOutput", map[string]any{"cmd": "ls"}, "ls"},
		{"grep short", "Grep", map[string]any{"pattern": "foo"}, "foo"},
		{"grep long truncates", "Grep", map[string]any{"pattern": strings.Repeat("p", 50)}, strings.Repeat("p", 37) + "..."},
		{"websearch", "WebSearch", map[string]any{"query": "golang"}, "golang"},
		{"task description", "Task", map[string]any{"description": "run tests"}, "run tests"},
		{"task prompt fallback", "Task", map[string]any{"prompt": "do a thing"}, "do a thing"},
		{"unknown tool", "SomeOtherTool", map[string]any{"x": "y"}, ""},
		{"missing key", "Read", map[string]any{}, ""},
		{"empty string value ignored", "Read", map[string]any{"file_path": ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summarizeToolInput(tt.tool, tt.input); got != tt.want {
				t.Errorf("summarizeToolInput(%q, %v) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestShortPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"short path unchanged", "/a/b.go", "/a/b.go"},
		{"long path with many segments truncates to last 3", "/home/user/project/src/internal/pkg/deep/nested/file.go", ".../deep/nested/file.go"},
		{"long path few segments no truncation under 60", "/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"long single-segment over 60 chars truncates", "/" + strings.Repeat("a", 80), "/" + strings.Repeat("a", 56) + "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortPath(tt.path); got != tt.want {
				t.Errorf("shortPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestTranslateMessage(t *testing.T) {
	tools := newToolCallState()

	t.Run("assistant message translates content", func(t *testing.T) {
		msg := claudecode.AssistantMessage{Content: []claudecode.ContentBlock{
			claudecode.TextBlock{Text: "hello"},
		}}

		got := translateMessage(msg, tools)
		if len(got) != 1 || got[0].AgentMessageChunk == nil || got[0].AgentMessageChunk.Content.Text.Text != "hello" {
			t.Fatalf("translateMessage(assistant) = %+v", got)
		}
	})

	t.Run("user message translates content", func(t *testing.T) {
		msg := claudecode.UserMessage{Content: []claudecode.ContentBlock{
			claudecode.TextBlock{Text: "hi"},
		}}

		got := translateMessage(msg, tools)
		if len(got) != 1 || got[0].AgentMessageChunk == nil || got[0].AgentMessageChunk.Content.Text.Text != "hi" {
			t.Fatalf("translateMessage(user) = %+v", got)
		}
	})

	t.Run("result message produces nothing", func(t *testing.T) {
		if got := translateMessage(claudecode.ResultMessage{}, tools); got != nil {
			t.Errorf("translateMessage(result) = %+v, want nil", got)
		}
	})
}

func TestToolResultContent(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want []string // expected text of each ToolCallContent
	}{
		{"empty raw", nil, nil},
		{"plain string", json.RawMessage(`"hello"`), []string{"hello"}},
		{"array of text blocks", json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`), []string{"a", "b"}},
		{"array skips non-text blocks", json.RawMessage(`[{"type":"image","text":"ignored"},{"type":"text","text":"kept"}]`), []string{"kept"}},
		{"unparseable falls back to raw string", json.RawMessage(`42`), []string{"42"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolResultContent(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("toolResultContent(%s) = %d items, want %d", tt.raw, len(got), len(tt.want))
			}

			for i, w := range tt.want {
				if got[i].Text == nil || got[i].Text.Text != w {
					t.Errorf("toolResultContent(%s)[%d] = %+v, want text %q", tt.raw, i, got[i], w)
				}
			}
		})
	}
}
