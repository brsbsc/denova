package codex

import (
	"runtime"
	"testing"

	"denova/config"
	"denova/internal/agents/runtime/external"
	"denova/internal/hostruntime"
)

func TestNativeConfigContainerSandbox(t *testing.T) {
	client := &Client{}

	for _, kind := range []string{config.AgentKindIDE, config.AgentKindGeneral} {
		t.Run("container-enabled-"+kind, func(t *testing.T) {
			input := external.Input{
				Mode:      external.OperationTurn,
				AgentKind: kind,
				Tools:     []external.Tool{{Name: "ask"}},
			}
			settings, err := client.nativeConfigForContainer(t.Context(), input, true)
			if err != nil {
				t.Fatalf("nativeConfigForContainer() error = %v", err)
			}
			if got, ok := settings["features.shell_tool"]; !ok || got != false {
				t.Fatalf("features.shell_tool = %#v (present=%t), want false", got, ok)
			}
			if got := settings["features.default_mode_request_user_input"]; got != true {
				t.Fatalf("features.default_mode_request_user_input = %#v, want true", got)
			}
			if _, disabled := settings["web_search"]; disabled {
				t.Fatalf("container mode unexpectedly disabled web_search: %#v", settings)
			}
		})

		t.Run("container-disabled-"+kind, func(t *testing.T) {
			input := external.Input{
				Mode:      external.OperationTurn,
				AgentKind: kind,
				Tools:     []external.Tool{{Name: "ask"}},
			}
			settings, err := client.nativeConfigForContainer(t.Context(), input, false)
			if err != nil {
				t.Fatalf("nativeConfigForContainer() error = %v", err)
			}
			if _, present := settings["features.shell_tool"]; present {
				t.Fatalf("non-container mode unexpectedly set features.shell_tool: %#v", settings)
			}
		})
	}

	t.Run("interactive-story-always-disables-shell", func(t *testing.T) {
		input := external.Input{
			Mode:      external.OperationTurn,
			AgentKind: config.AgentKindInteractiveStory,
		}
		for _, containerSandbox := range []bool{false, true} {
			settings, err := client.nativeConfigForContainer(t.Context(), input, containerSandbox)
			if err != nil {
				t.Fatalf("nativeConfigForContainer(story, %t) error = %v", containerSandbox, err)
			}
			if got := settings["features.shell_tool"]; got != false {
				t.Fatalf("story features.shell_tool = %#v, want false", got)
			}
		}
	})

	t.Run("host-env-integration", func(t *testing.T) {
		input := external.Input{
			Mode:      external.OperationTurn,
			AgentKind: config.AgentKindIDE,
		}
		t.Setenv(hostruntime.EnvContainerSandbox, "")
		settings, err := client.nativeConfig(t.Context(), input)
		if err != nil {
			t.Fatalf("nativeConfig(unset) error = %v", err)
		}
		if _, present := settings["features.shell_tool"]; present {
			t.Fatalf("unset env unexpectedly set features.shell_tool: %#v", settings)
		}

		t.Setenv(hostruntime.EnvContainerSandbox, "1")
		settings, err = client.nativeConfig(t.Context(), input)
		if err != nil {
			t.Fatalf("nativeConfig(1) error = %v", err)
		}
		got, present := settings["features.shell_tool"]
		if runtime.GOOS == "linux" {
			if !present || got != false {
				t.Fatalf("Linux container env did not disable features.shell_tool: %#v", settings)
			}
		} else if present {
			t.Fatalf("non-Linux host unexpectedly enabled container sandbox: %#v", settings)
		}
	})
}
