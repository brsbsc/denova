package antigravity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"denova/internal/hostruntime"
)

func TestValidateMinimumVersionContainerAndHost(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		goos             string
		containerSandbox bool
		wantErr          bool
		wantVersion      string
	}{
		{name: "linux host allows 1.4.2", raw: "1.4.2\n", goos: "linux", containerSandbox: false, wantVersion: "1.4.2"},
		{name: "linux host rejects 1.4.1", raw: "1.4.1", goos: "linux", containerSandbox: false, wantErr: true},
		{name: "linux container rejects 1.4.2", raw: "1.4.2", goos: "linux", containerSandbox: true, wantErr: true},
		{name: "linux container rejects 1.4.3", raw: "1.4.3", goos: "linux", containerSandbox: true, wantErr: true},
		{name: "linux container allows 1.4.4", raw: "1.4.4", goos: "linux", containerSandbox: true, wantVersion: "1.4.4"},
		{name: "linux container allows 1.5.18", raw: "1.5.18", goos: "linux", containerSandbox: true, wantVersion: "1.5.18"},
		{name: "windows host rejects 1.4.2", raw: "1.4.2", goos: "windows", containerSandbox: false, wantErr: true},
		{name: "windows host allows 1.4.4", raw: "1.4.4", goos: "windows", containerSandbox: false, wantVersion: "1.4.4"},
		{name: "invalid semver rejected", raw: "dev", goos: "linux", containerSandbox: false, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			version, err := validateMinimumVersionFor(tc.raw, tc.goos, tc.containerSandbox)
			if tc.wantErr {
				if !errors.Is(err, ErrVersionUnsupported) {
					t.Fatalf("validateMinimumVersionFor(%q, %q, %t) err = %v, want ErrVersionUnsupported", tc.raw, tc.goos, tc.containerSandbox, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateMinimumVersionFor(%q, %q, %t) unexpected err: %v", tc.raw, tc.goos, tc.containerSandbox, err)
			}
			if version.String() != tc.wantVersion {
				t.Fatalf("version = %q, want %q", version.String(), tc.wantVersion)
			}
		})
	}
}

func TestContainerSandboxHooksAndSessionSettings(t *testing.T) {
	restoreContainer := containerSandboxEnabled
	restoreSandbox := verifyOSSandbox
	t.Cleanup(func() {
		containerSandboxEnabled = restoreContainer
		verifyOSSandbox = restoreSandbox
	})

	containerSandboxEnabled = func() bool { return true }
	if _, err := validateMinimumVersion("1.4.2"); !errors.Is(err, ErrVersionUnsupported) {
		t.Fatalf("container mode accepted 1.4.2: %v", err)
	}
	if version, err := validateMinimumVersion("1.4.4"); err != nil || version.String() != "1.4.4" {
		t.Fatalf("container mode rejected 1.4.4: %v %v", version, err)
	}

	// Container mode skips OS userns probing, but ambient hook checks must
	// remain unconditionally enforced for both global and workspace configs.
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	verifyOSSandbox = func() error { return nil }

	if err := checkIsolation(workspace); err != nil {
		t.Fatalf("clean container isolation failed: %v", err)
	}

	globalHook := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(globalHook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalHook, []byte(`{"active":{"SessionStart":[{"command":"blocked"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkIsolation(workspace); !errors.Is(err, ErrUnsafeCustomization) {
		t.Fatalf("container isolation allowed active global hook: %v", err)
	}
	if err := os.Remove(globalHook); err != nil {
		t.Fatal(err)
	}

	workspaceHook := filepath.Join(workspace, ".agents", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(workspaceHook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceHook, []byte(`{"ws":{"PreInvocation":[{"command":"blocked"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkIsolation(workspace); !errors.Is(err, ErrUnsafeCustomization) {
		t.Fatalf("container isolation allowed active workspace hook: %v", err)
	}

	// Session settings must continue enforcing terminal sandbox and deny rules
	// in container mode so CLI >= 1.4.4 retains safe degradation semantics.
	rawSettings, err := sessionSettingsJSON(true)
	if err != nil {
		t.Fatal(err)
	}
	var parsed sessionSettings
	if err := json.Unmarshal([]byte(rawSettings), &parsed); err != nil {
		t.Fatal(err)
	}
	if !parsed.EnableTerminalSandbox || parsed.ToolPermission != "request-review" || parsed.AllowNonWorkspaceAccess {
		t.Fatalf("unexpected container session settings: %+v", parsed)
	}
	for _, denied := range []string{"write_file(*)", "command(*)", "execute_url(*)", "read_url(*)"} {
		if !slices.Contains(parsed.Permissions.Deny, denied) {
			t.Fatalf("missing required deny rule %q in %+v", denied, parsed.Permissions.Deny)
		}
	}
}

func TestLinuxContainerSandboxSkipsUsernsProbeButChecksHooks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only detectOSSandbox check")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(hostruntime.EnvContainerSandbox, "1")

	if err := detectOSSandbox(); err != nil {
		t.Fatalf("detectOSSandbox() in container mode failed: %v", err)
	}

	hookPath := filepath.Join(home, ".gemini", "config", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(`{"active":{"SessionStart":[{"command":"blocked"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkIsolation(""); !errors.Is(err, ErrUnsafeCustomization) {
		t.Fatalf("Linux container checkIsolation allowed active hook: %v", err)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DENOVA_ANTIGRAVITY_FIXTURE", "old-windows")
	if _, err := Connect(t.Context(), ProcessOptions{Executable: executable, Args: []string{"-test.run=TestAntigravityFixtureProcess", "--"}}); !errors.Is(err, ErrVersionUnsupported) {
		t.Fatalf("Linux container Connect accepted 1.4.2 CLI: %v", err)
	}

	t.Setenv(hostruntime.EnvContainerSandbox, "")
	client, err := Connect(t.Context(), ProcessOptions{Executable: executable, Args: []string{"-test.run=TestAntigravityFixtureProcess", "--"}})
	if err != nil {
		t.Fatalf("non-container Linux Connect rejected 1.4.2 CLI: %v", err)
	}
	_ = client.Close()
}
