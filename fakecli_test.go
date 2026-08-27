package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"testing"
	"time"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// TestMain re-execs the test binary itself as the fake `claude` CLI when
// ACP_FAKE_CLI=1, following the standard Go helper-process pattern (same
// approach as claude-agent-sdk-go's fakecli_test.go, replicated here
// because the SDK's fake is unexported and our Agent spawns real
// claudecode.Clients in tests).
func TestMain(m *testing.M) {
	if os.Getenv("ACP_FAKE_CLI") == "1" {
		runFakeCLI()
		return
	}

	os.Exit(m.Run())
}

// fakeCLIOptions returns SDK options pointing a Client at this test binary
// running as the fake CLI for the given scenario.
func fakeCLIOptions(t *testing.T, scenario string) []claudecode.Option {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	return []claudecode.Option{
		claudecode.WithCLIPath(self),
		claudecode.WithEnv(
			"ACP_FAKE_CLI=1",
			"ACP_FAKE_SCENARIO="+scenario,
		),
	}
}

func runFakeCLI() {
	if len(os.Args) > 1 && os.Args[1] == "-v" {
		fmt.Println("2.1.0")
		os.Exit(0)
	}

	scenario := os.Getenv("ACP_FAKE_SCENARIO")

	stdin := bufio.NewScanner(os.Stdin)
	stdin.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	writeLine := func(v any) {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}

		fmt.Printf("%s\n", data)
	}

	controlResponse := func(requestID string, response any) {
		writeLine(map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "success",
				"request_id": requestID,
				"response":   response,
			},
		})
	}

	ackInitialize := func() {
		if !stdin.Scan() {
			panic("fake cli: stdin closed before initialize")
		}

		var env struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
			panic(fmt.Sprintf("fake cli: expected initialize control_request, got %q", stdin.Text()))
		}

		controlResponse(env.RequestID, map[string]any{"success": true})
	}

	consumePrompt := func() string {
		if !stdin.Scan() {
			panic("fake cli: stdin closed before prompt")
		}

		var env struct {
			Type      string          `json:"type"`
			SessionID string          `json:"session_id"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "user" {
			panic(fmt.Sprintf("fake cli: expected user prompt, got %q", stdin.Text()))
		}

		return env.SessionID
	}

	// readCanUseToolResponse waits for the client's control_response
	// answering a can_use_tool request and returns the decision behavior.
	readCanUseToolResponse := func(requestID string) string {
		for stdin.Scan() {
			var env struct {
				Type     string `json:"type"`
				Response struct {
					Subtype   string          `json:"subtype"`
					RequestID string          `json:"request_id"`
					Response  json.RawMessage `json:"response"`
				} `json:"response"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_response" {
				continue
			}

			if env.Response.RequestID != requestID {
				continue
			}

			var d struct {
				Behavior string `json:"behavior"`
			}

			_ = json.Unmarshal(env.Response.Response, &d)

			return d.Behavior
		}

		panic("fake cli: stdin closed waiting for can_use_tool response")
	}

	sendCanUseTool := func(requestID, toolName string, input map[string]any, toolUseID string) string {
		writeLine(map[string]any{
			"type":       "control_request",
			"request_id": requestID,
			"request": map[string]any{
				"subtype":     "can_use_tool",
				"tool_name":   toolName,
				"input":       input,
				"tool_use_id": toolUseID,
			},
		})

		return readCanUseToolResponse(requestID)
	}

	assistant := func(blocks []any) {
		writeLine(map[string]any{
			"type":       "assistant",
			"session_id": "sess-1",
			"message": map[string]any{
				"model":   "claude-fake",
				"content": blocks,
			},
		})
	}

	result := func(extra map[string]any) {
		m := map[string]any{
			"type":       "result",
			"subtype":    "success",
			"is_error":   false,
			"num_turns":  1,
			"session_id": "sess-1",
			"result":     "done",
		}
		maps.Copy(m, extra)

		writeLine(m)
	}

	textBlock := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

	switch scenario {
	case "simple":
		ackInitialize()
		consumePrompt()
		assistant([]any{textBlock("hello there")})
		result(nil)

	case "tool_turn":
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
		})

		behavior := sendCanUseTool("req-1", "Bash", map[string]any{"command": "go test ./..."}, "tool-1")
		if behavior != "allow" {
			panic("fake cli: expected allow")
		}

		writeLine(map[string]any{
			"type":       "user",
			"session_id": "sess-1",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "tool-1",
						"content":     "go test ok",
					},
				},
			},
		})
		assistant([]any{textBlock("all done")})
		result(nil)

	case "tool_fail":
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-1", "name": "Read", "input": map[string]any{"file_path": "/tmp/x.go"}},
		})

		behavior := sendCanUseTool("req-1", "Read", map[string]any{"file_path": "/tmp/x.go"}, "tool-1")
		if behavior != "allow" {
			panic("fake cli: expected allow")
		}

		writeLine(map[string]any{
			"type":       "user",
			"session_id": "sess-1",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type":        "tool_result",
						"tool_use_id": "tool-1",
						"content":     "boom",
						"is_error":    true,
					},
				},
			},
		})
		result(nil)

	case "mcp_tool":
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-mcp", "name": "mcp__myserver__mytool", "input": map[string]any{}},
		})

		behavior := sendCanUseTool("req-1", "mcp__myserver__mytool", map[string]any{}, "tool-mcp")
		if behavior != "allow" {
			panic("fake cli: expected allow")
		}

		writeLine(map[string]any{
			"type":       "user",
			"session_id": "sess-1",
			"message": map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "tool-mcp", "content": "mcp ok"},
				},
			},
		})
		result(nil)

	case "tool_denied":
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "rm -rf /"}},
		})

		behavior := sendCanUseTool("req-1", "Bash", map[string]any{"command": "rm -rf /"}, "tool-1")
		if behavior != "deny" {
			panic("fake cli: expected deny")
		}

		result(map[string]any{"permission_denials": []any{map[string]any{"tool": "Bash"}}})

	case "thinking":
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "thinking", "thinking": "hmm, pondering", "signature": "sig"},
			textBlock("answer"),
		})
		result(nil)

	case "deny_permission":
		// can_use_tool answered with deny by the ACP client; the turn ends
		// refusal-shaped.
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "echo hi"}},
		})

		behavior := sendCanUseTool("req-1", "Bash", map[string]any{"command": "echo hi"}, "tool-1")
		if behavior != "deny" {
			panic("fake cli: expected deny")
		}

		result(map[string]any{"permission_denials": []any{map[string]any{"tool": "Bash"}}})

	case "hang_mid_permission":
		// Sends a can_use_tool and never finishes the turn unless the
		// client allows it; used with session/cancel to assert the
		// permission request resolves cancelled. After the can_use_tool
		// response (allow), we simply wait for the interrupt control
		// request, ack it, and finish with an interrupted result.
		ackInitialize()
		consumePrompt()
		assistant([]any{
			map[string]any{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": map[string]any{"command": "sleep 100"}},
		})
		// Send the can_use_tool control request; the client's ACP-side
		// permission responder never answers (test-side select{}), so the
		// turn stays pending until session/cancel interrupts it.
		writeLine(map[string]any{
			"type":       "control_request",
			"request_id": "req-1",
			"request": map[string]any{
				"subtype":     "can_use_tool",
				"tool_name":   "Bash",
				"input":       map[string]any{"command": "sleep 100"},
				"tool_use_id": "tool-1",
			},
		})

		sawInterrupt := false

		for stdin.Scan() {
			var env struct {
				Type      string          `json:"type"`
				RequestID string          `json:"request_id"`
				Request   json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil {
				continue
			}

			if env.Type == "control_request" {
				var inner struct {
					Subtype string `json:"subtype"`
				}

				_ = json.Unmarshal(env.Request, &inner)
				if inner.Subtype == "interrupt" {
					controlResponse(env.RequestID, map[string]any{})

					sawInterrupt = true

					break
				}

				continue
			}

			if env.Type == "control_response" {
				// Client answered our can_use_tool; ignore and keep waiting
				// for the interrupt (test cancels regardless).
				continue
			}
		}

		_ = sawInterrupt

		result(map[string]any{"stop_reason": "interrupted", "result": "interrupted"})

	case "interruptible":
		// Streams one message, waits for interrupt, acks, finishes
		// interrupted (mirrors the SDK's interruptible scenario).
		ackInitialize()
		consumePrompt()
		assistant([]any{textBlock("working")})

		for stdin.Scan() {
			var env struct {
				Type      string          `json:"type"`
				RequestID string          `json:"request_id"`
				Request   json.RawMessage `json:"request"`
			}
			if err := json.Unmarshal(stdin.Bytes(), &env); err != nil || env.Type != "control_request" {
				continue
			}

			var inner struct {
				Subtype string `json:"subtype"`
			}

			_ = json.Unmarshal(env.Request, &inner)
			if inner.Subtype == "interrupt" {
				controlResponse(env.RequestID, map[string]any{})
				break
			}
		}

		result(map[string]any{"stop_reason": "interrupted", "result": "interrupted"})

	default:
		panic("fake cli: unknown scenario " + scenario)
	}

	// Keep the process alive briefly so trailing writes from the client
	// don't hit a closed pipe and turn into noisy failures.
	time.Sleep(50 * time.Millisecond)
}
