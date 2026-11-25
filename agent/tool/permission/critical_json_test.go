package permission

import (
	"context"
	"encoding/json"
	"testing"

	agenttool "github.com/alfredxw/denova/agent/tool"
)

func TestCriticalToolRequestUnmarshalsJSONEscapedCommands(t *testing.T) {
	policy := FullAccess()
	shellDescriptor := agenttool.ToolDescriptor{
		Source:        agenttool.ToolSourceShell,
		MutationScope: agenttool.ToolMutationExternal,
	}

	blockedPayloads := []string{
		`{"command":"Remove-Item C:\\ -Recurse"}`,
		`{"command":"Remove-Item   C:\\   -Recurse -Force"}`,
		`{"command":"remove-item c:/ -recurse"}`,
		`{"command":"\u0072m -rf /"}`,
		`{"nested":{"cmd":"dd   if=/dev/zero   of=/dev/sda"}}`,
		`malformed json with rm -rf / fallback`,
	}
	for _, raw := range blockedPayloads {
		decision, err := policy.Evaluate(context.Background(), PermissionRequest{
			CallID:     "call-1",
			Tool:       "pwsh",
			Arguments:  json.RawMessage(raw),
			Descriptor: shellDescriptor,
		})
		if err != nil {
			t.Fatalf("payload %s error: %v", raw, err)
		}
		if decision.Kind != PermissionBlock {
			t.Fatalf("payload %s decision = %q, want %q", raw, decision.Kind, PermissionBlock)
		}
	}

	safeDecision, err := policy.Evaluate(context.Background(), PermissionRequest{
		CallID:     "call-safe",
		Tool:       "pwsh",
		Arguments:  json.RawMessage(`{"command":"Get-ChildItem C:\\Users"}`),
		Descriptor: shellDescriptor,
	})
	if err != nil || safeDecision.Kind != PermissionAllow {
		t.Fatalf("safe command decision = %#v, err = %v", safeDecision, err)
	}
}
