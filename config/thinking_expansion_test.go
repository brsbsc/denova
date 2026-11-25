package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultSettingsOmitsLegacyAutoExpandThinking(t *testing.T) {
	def := DefaultSettings()
	if def.AutoExpandThinking != nil {
		t.Fatalf("DefaultSettings().AutoExpandThinking = %v, want nil", *def.AutoExpandThinking)
	}
	if def.AgentTraceDisplay != "compact" {
		t.Fatalf("DefaultSettings().AgentTraceDisplay = %q, want %q", def.AgentTraceDisplay, "compact")
	}
	data, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["auto_expand_thinking"]; exists {
		t.Fatalf("DefaultSettings() should not emit auto_expand_thinking: %s", data)
	}
	if fields["agent_trace_display"] != "compact" {
		t.Fatalf("DefaultSettings() agent_trace_display = %v, want compact", fields["agent_trace_display"])
	}
}

func TestLegacyAutoExpandThinkingResolvesAgentTraceDisplayAndRespectsExplicitValue(t *testing.T) {
	tests := []struct {
		name              string
		global            Settings
		userTOML          string
		wantEffective     string
		wantInheritedUser string
	}{
		{
			name:              "legacy user auto_expand_thinking true maps to live",
			userTOML:          "theme = \"dark\"\nauto_expand_thinking = true\n",
			wantEffective:     "live",
			wantInheritedUser: "compact",
		},
		{
			name:              "legacy user auto_expand_thinking false stays compact",
			userTOML:          "auto_expand_thinking = false\n",
			wantEffective:     "compact",
			wantInheritedUser: "compact",
		},
		{
			name:              "explicit user agent_trace_display compact wins over legacy true",
			userTOML:          "auto_expand_thinking = true\nagent_trace_display = \"compact\"\n",
			wantEffective:     "compact",
			wantInheritedUser: "compact",
		},
		{
			name:              "explicit user agent_trace_display live wins over legacy false",
			userTOML:          "auto_expand_thinking = false\nagent_trace_display = \"live\"\n",
			wantEffective:     "live",
			wantInheritedUser: "compact",
		},
		{
			name:              "legacy global auto_expand_thinking true maps to live when unset in user",
			global:            Settings{AutoExpandThinking: boolPtr(true)},
			userTOML:          "theme = \"light\"\n",
			wantEffective:     "live",
			wantInheritedUser: "live",
		},
		{
			name:              "legacy user false overrides legacy global true",
			global:            Settings{AutoExpandThinking: boolPtr(true)},
			userTOML:          "auto_expand_thinking = false\n",
			wantEffective:     "compact",
			wantInheritedUser: "live",
		},
		{
			name:              "explicit user agent_trace_display wins over legacy global true",
			global:            Settings{AutoExpandThinking: boolPtr(true)},
			userTOML:          "agent_trace_display = \"compact\"\n",
			wantEffective:     "compact",
			wantInheritedUser: "live",
		},
		{
			name:              "explicit global agent_trace_display compact wins over legacy user true",
			global:            Settings{AgentTraceDisplay: "compact"},
			userTOML:          "auto_expand_thinking = true\n",
			wantEffective:     "compact",
			wantInheritedUser: "compact",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			novaDir := t.TempDir()
			if err := os.WriteFile(UserConfigPath(novaDir), []byte(tc.userTOML), 0o644); err != nil {
				t.Fatal(err)
			}
			layered, err := LoadLayeredWithGlobal(novaDir, "", tc.global)
			if err != nil {
				t.Fatal(err)
			}
			if layered.Effective.AgentTraceDisplay != tc.wantEffective {
				t.Fatalf("Effective.AgentTraceDisplay = %q, want %q", layered.Effective.AgentTraceDisplay, tc.wantEffective)
			}
			if layered.Inherited.User.AgentTraceDisplay != tc.wantInheritedUser {
				t.Fatalf("Inherited.User.AgentTraceDisplay = %q, want %q", layered.Inherited.User.AgentTraceDisplay, tc.wantInheritedUser)
			}
			if layered.Inherited.Workspace.AgentTraceDisplay != tc.wantEffective {
				t.Fatalf("Inherited.Workspace.AgentTraceDisplay = %q, want %q", layered.Inherited.Workspace.AgentTraceDisplay, tc.wantEffective)
			}
		})
	}
}

func TestAgentTraceDisplayPersistsAndRemainsUserScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	settings := Settings{Theme: "dark"}
	for _, tc := range []struct {
		patch string
		want  string
	}{
		{patch: `{"agent_trace_display":"live"}`, want: "live"},
		{patch: `{"agent_trace_display":"compact"}`, want: "compact"},
		{patch: `{"agent_trace_display":null}`, want: "compact"},
	} {
		next, err := ApplySettingsMergePatch(settings, json.RawMessage(tc.patch))
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteSettingsFile(path, next); err != nil {
			t.Fatal(err)
		}
		stored, err := ReadSettingsFile(path)
		if err != nil {
			t.Fatal(err)
		}
		effective := Merge(DefaultSettings(), stored)
		if effective.AgentTraceDisplay != tc.want || effective.Theme != "dark" {
			t.Fatalf("patch %s produced AgentTraceDisplay=%q Theme=%q, want %q / dark", tc.patch, effective.AgentTraceDisplay, effective.Theme, tc.want)
		}
		settings = stored
	}
	if err := ValidateWorkspaceSettingsPatch(json.RawMessage(`{"auto_expand_thinking":true}`)); err == nil {
		t.Fatal("legacy auto_expand_thinking must remain user-scoped")
	}
	if err := ValidateWorkspaceSettingsPatch(json.RawMessage(`{"agent_trace_display":"live"}`)); err == nil {
		t.Fatal("agent_trace_display must remain user-scoped")
	}
}
