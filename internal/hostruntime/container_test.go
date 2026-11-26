package hostruntime

import "testing"

func withHostPlatform(t *testing.T, goos, goarch string) {
	t.Helper()
	prevGOOS, prevGOARCH := hostGOOS, hostGOARCH
	hostGOOS = func() string { return goos }
	hostGOARCH = func() string { return goarch }
	t.Cleanup(func() {
		hostGOOS = prevGOOS
		hostGOARCH = prevGOARCH
	})
}

func TestContainerSandboxEnabled(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  string
		want bool
	}{
		{name: "linux unset", goos: "linux", env: "", want: false},
		{name: "linux zero", goos: "linux", env: "0", want: false},
		{name: "linux false", goos: "linux", env: "false", want: false},
		{name: "linux yes rejected", goos: "linux", env: "yes", want: false},
		{name: "linux one", goos: "linux", env: "1", want: true},
		{name: "linux true", goos: "linux", env: "true", want: true},
		{name: "linux TRUE", goos: "linux", env: "TRUE", want: true},
		{name: "linux trimmed True", goos: "linux", env: " True ", want: true},
		{name: "windows one", goos: "windows", env: "1", want: false},
		{name: "windows true", goos: "windows", env: "true", want: false},
		{name: "darwin one", goos: "darwin", env: "1", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHostPlatform(t, tc.goos, "amd64")
			t.Setenv(EnvContainerSandbox, tc.env)
			if got := ContainerSandboxEnabled(); got != tc.want {
				t.Fatalf("ContainerSandboxEnabled() = %t, want %t (goos=%q env=%q)", got, tc.want, tc.goos, tc.env)
			}
		})
	}
}

func TestManagedAgentCLIDirAndEnabled(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		goarch string
		env    string
		want   string
	}{
		{name: "linux amd64 absolute", goos: "linux", goarch: "amd64", env: "/data/.local/bin", want: "/data/.local/bin"},
		{name: "linux arm64 cleaned", goos: "linux", goarch: "arm64", env: " /data/tmp/../.local/bin/ ", want: "/data/.local/bin"},
		{name: "linux empty", goos: "linux", goarch: "amd64", env: "", want: ""},
		{name: "linux whitespace", goos: "linux", goarch: "amd64", env: "   ", want: ""},
		{name: "linux relative path rejected", goos: "linux", goarch: "amd64", env: "data/.local/bin", want: ""},
		{name: "linux dot relative rejected", goos: "linux", goarch: "amd64", env: "./bin", want: ""},
		{name: "linux windows path rejected", goos: "linux", goarch: "amd64", env: `C:\data\bin`, want: ""},
		{name: "linux unsupported arch", goos: "linux", goarch: "386", env: "/data/.local/bin", want: ""},
		{name: "windows amd64 disabled", goos: "windows", goarch: "amd64", env: "/data/.local/bin", want: ""},
		{name: "darwin arm64 disabled", goos: "darwin", goarch: "arm64", env: "/data/.local/bin", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHostPlatform(t, tc.goos, tc.goarch)
			t.Setenv(EnvManagedAgentCLIDir, tc.env)
			if got := ManagedAgentCLIDir(); got != tc.want {
				t.Fatalf("ManagedAgentCLIDir() = %q, want %q", got, tc.want)
			}
			wantEnabled := tc.want != ""
			if got := ManagedAgentCLIsEnabled(); got != wantEnabled {
				t.Fatalf("ManagedAgentCLIsEnabled() = %t, want %t", got, wantEnabled)
			}
		})
	}
}

func TestWebAuthEnabled(t *testing.T) {
	cases := []struct {
		name       string
		goos       string
		goarch     string
		sandboxEnv string
		cliDirEnv  string
		want       bool
	}{
		{name: "linux neither", goos: "linux", goarch: "amd64", want: false},
		{name: "linux sandbox only", goos: "linux", goarch: "amd64", sandboxEnv: "1", want: true},
		{name: "linux managed dir only", goos: "linux", goarch: "amd64", cliDirEnv: "/data/.local/bin", want: true},
		{name: "linux both", goos: "linux", goarch: "arm64", sandboxEnv: "true", cliDirEnv: "/data/.local/bin", want: true},
		{name: "linux unsupported arch managed dir only", goos: "linux", goarch: "riscv64", cliDirEnv: "/data/.local/bin", want: false},
		{name: "linux unsupported arch sandbox enabled", goos: "linux", goarch: "riscv64", sandboxEnv: "1", want: true},
		{name: "windows both disabled", goos: "windows", goarch: "amd64", sandboxEnv: "1", cliDirEnv: "/data/.local/bin", want: false},
		{name: "darwin both disabled", goos: "darwin", goarch: "arm64", sandboxEnv: "true", cliDirEnv: "/data/.local/bin", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withHostPlatform(t, tc.goos, tc.goarch)
			t.Setenv(EnvContainerSandbox, tc.sandboxEnv)
			t.Setenv(EnvManagedAgentCLIDir, tc.cliDirEnv)
			if got := WebAuthEnabled(); got != tc.want {
				t.Fatalf("WebAuthEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}
