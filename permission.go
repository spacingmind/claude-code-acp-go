package acp

import (
	"context"
	"encoding/json"

	claudecode "github.com/spacingmind/claude-agent-sdk-go"
)

// acpPermissionPolicy implements claudecode.PermissionPolicy by asking the
// ACP client via session/request_permission instead of deciding locally
// (AC 17-19). Options are a fixed binary allow/deny set for this phase.
type acpPermissionPolicy struct {
	agent     *Agent
	sessionID string
	session   *session
}

var pendingStatus = ToolCallStatusPending

func permissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: "allow", Name: "Allow", Kind: PermissionOptionKindAllowOnce},
		{OptionID: "deny", Name: "Deny", Kind: PermissionOptionKindRejectOnce},
	}
}

func (p *acpPermissionPolicy) Decide(ctx context.Context, req claudecode.CanUseToolRequest) (bool, map[string]any, string, []claudecode.PermissionUpdate, bool, error) {
	toolCall := ToolCallUpdate{
		ToolCallID: req.ToolUseID,
		Title:      p.session.tools.title(req.ToolUseID),
		Status:     &pendingStatus,
	}

	// The round trip is scoped to the caller's context AND the session's
	// current turn context, so session/cancel unblocks it (AC 12).
	turnCtx := p.session.currentTurnCtx()

	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopTurn := make(chan struct{})

	go func() {
		select {
		case <-turnCtx.Done():
			cancel()
		case <-stopTurn:
		}
	}()

	defer close(stopTurn)

	raw, err := p.agent.conn.Call(callCtx, MethodRequestPermission, RequestPermissionRequest{
		SessionID: p.sessionID,
		ToolCall:  toolCall,
		Options:   permissionOptions(),
	})
	if err != nil {
		if turnCtx.Err() != nil || p.session.wasCancelled() {
			return false, nil, "permission request was cancelled", nil, false, nil
		}

		return false, nil, "", nil, false, err
	}

	var resp RequestPermissionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, nil, "", nil, false, err
	}

	switch {
	case resp.Outcome.Cancelled:
		return false, nil, "permission request was cancelled", nil, false, nil
	case resp.Outcome.Selected != nil && resp.Outcome.Selected.OptionID == "allow":
		return true, nil, "", nil, false, nil
	case resp.Outcome.Selected != nil && resp.Outcome.Selected.OptionID == "deny":
		return false, nil, "denied by user", nil, false, nil
	default:
		return false, nil, "unrecognized permission outcome", nil, false, nil
	}
}
