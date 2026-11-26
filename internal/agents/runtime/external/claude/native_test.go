package claude

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"denova/config"
	"denova/internal/agents/runtime/external"
	"denova/internal/hostruntime"
)

func TestNativeElicitationControlRoundTrip(t *testing.T) {
	host := &testHost{call: func(_ context.Context, call external.ToolCall) (external.ToolResult, error) {
		if call.Name != "ask" || !strings.Contains(string(call.Arguments), "Include references") {
			t.Fatalf("elicitation did not reach the product question: %+v", call)
		}
		return external.ToolResult{Success: true, Text: `{"status":"answered","answers":[{"question_id":"question_0","custom_input":"true"}]}`}, nil
	}}
	run := streamRun{host: host, input: external.Input{Tools: []external.Tool{{Name: "ask"}}}}
	// This is the Claude control envelope, not the camelCase MCP wire format.
	var frame streamFrame
	if err := json.Unmarshal([]byte(`{"type":"control_request","request_id":"form-1","request":{"subtype":"elicitation","mcp_server_name":"research","message":"Research options","requested_schema":{"type":"object","properties":{"references":{"type":"boolean","title":"Include references"}},"required":["references"]}}}`), &frame); err != nil {
		t.Fatal(err)
	}
	body, err := run.control(t.Context(), frame)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"type":"control_response","response":{"subtype":"success","request_id":"form-1","response":{"action":"accept","content":{"references":true}}}}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || len(host.calls) != 1 {
		t.Fatalf("elicitation response=%s calls=%d", body, len(host.calls))
	}
}

func parseNativeSettings(t *testing.T, args []string) map[string]any {
	t.Helper()
	index := slices.Index(args, "--settings")
	if index < 0 || index+1 >= len(args) {
		t.Fatalf("missing --settings flag in %v", args)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(args[index+1]), &settings); err != nil {
		t.Fatalf("unmarshal --settings JSON %q: %v", args[index+1], err)
	}
	return settings
}

func TestNativeArgumentsContainerSandbox(t *testing.T) {
	baseInput := external.Input{
		Mode:      external.OperationTurn,
		Workspace: "/data/projects/book",
		StoreRoot: "/data/.denova/stores/s1",
		DataRoot:  "/data/.denova",
	}

	for _, kind := range []string{config.AgentKindIDE, config.AgentKindGeneral} {
		input := baseInput
		input.AgentKind = kind

		t.Run("linux-container-omits-bash-and-sandbox-"+kind, func(t *testing.T) {
			args, names := nativeArgumentsForHost(input, "linux", true)
			if slices.Contains(names, "Bash") {
				t.Fatalf("container mode unexpectedly enabled Bash: %v", names)
			}
			for _, required := range []string{"Read", "Glob", "Grep", "WebFetch", "WebSearch", "AskUserQuestion"} {
				if !slices.Contains(names, required) {
					t.Fatalf("container mode dropped required native tool %q: %v", required, names)
				}
			}
			settings := parseNativeSettings(t, args)
			if _, present := settings["sandbox"]; present {
				t.Fatalf("container mode unexpectedly configured settings.sandbox: %#v", settings)
			}
			if settings["disableAllHooks"] != true || settings["autoMemoryEnabled"] != false {
				t.Fatalf("unexpected base settings: %#v", settings)
			}
		})

		t.Run("linux-host-enables-bash-and-sandbox-"+kind, func(t *testing.T) {
			args, names := nativeArgumentsForHost(input, "linux", false)
			if !slices.Contains(names, "Bash") {
				t.Fatalf("non-container Linux did not enable Bash: %v", names)
			}
			settings := parseNativeSettings(t, args)
			sandbox, ok := settings["sandbox"].(map[string]any)
			if !ok || sandbox["enabled"] != true || sandbox["failIfUnavailable"] != true || sandbox["allowUnsandboxedCommands"] != false {
				t.Fatalf("non-container Linux missing enforced sandbox settings: %#v", settings)
			}
		})

		t.Run("windows-host-omits-bash-and-sandbox-"+kind, func(t *testing.T) {
			args, names := nativeArgumentsForHost(input, "windows", false)
			if slices.Contains(names, "Bash") {
				t.Fatalf("Windows unexpectedly enabled Bash: %v", names)
			}
			settings := parseNativeSettings(t, args)
			if _, present := settings["sandbox"]; present {
				t.Fatalf("Windows unexpectedly configured settings.sandbox: %#v", settings)
			}
		})
	}

	t.Run("interactive-story-never-enables-bash", func(t *testing.T) {
		input := baseInput
		input.AgentKind = config.AgentKindInteractiveStory
		for _, containerSandbox := range []bool{false, true} {
			args, names := nativeArgumentsForHost(input, "linux", containerSandbox)
			if slices.Contains(names, "Bash") || slices.Contains(names, "AskUserQuestion") {
				t.Fatalf("interactive story enabled forbidden tools (container=%t): %v", containerSandbox, names)
			}
			if !slices.Contains(args, "--strict-mcp-config") {
				t.Fatalf("interactive story missing --strict-mcp-config: %v", args)
			}
			settings := parseNativeSettings(t, args)
			if _, present := settings["sandbox"]; present {
				t.Fatalf("interactive story unexpectedly set sandbox: %#v", settings)
			}
		}
	})

	t.Run("host-env-integration", func(t *testing.T) {
		input := baseInput
		input.AgentKind = config.AgentKindIDE
		t.Setenv(hostruntime.EnvContainerSandbox, "1")
		args, names := nativeArguments(input)
		if slices.Contains(names, "Bash") {
			t.Fatalf("nativeArguments with container env enabled Bash on %s: %v", runtime.GOOS, names)
		}
		settings := parseNativeSettings(t, args)
		if _, present := settings["sandbox"]; present {
			t.Fatalf("nativeArguments with container env set sandbox on %s: %#v", runtime.GOOS, settings)
		}

		if runtime.GOOS == "linux" {
			t.Setenv(hostruntime.EnvContainerSandbox, "")
			args, names = nativeArguments(input)
			if !slices.Contains(names, "Bash") {
				t.Fatalf("non-container Linux nativeArguments dropped Bash: %v", names)
			}
			settings = parseNativeSettings(t, args)
			if _, present := settings["sandbox"]; !present {
				t.Fatalf("non-container Linux nativeArguments dropped sandbox: %#v", settings)
			}
		}
	})
}
