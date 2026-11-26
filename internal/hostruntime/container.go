package hostruntime

import (
	"os"
	"path"
	"runtime"
	"strings"
)

const (
	// EnvContainerSandbox enables container-adapted external runtime sandboxing
	// on Linux when default Docker seccomp blocks nested user namespaces.
	EnvContainerSandbox = "DENOVA_CONTAINER_SANDBOX"

	// EnvManagedAgentCLIDir configures the persistent Linux directory where Denova
	// installs and updates managed external agent CLI binaries.
	EnvManagedAgentCLIDir = "DENOVA_MANAGED_CLI_DIR"
)

var (
	hostGOOS   = func() string { return runtime.GOOS }
	hostGOARCH = func() string { return runtime.GOARCH }
)

// ContainerSandboxEnabled reports whether Linux container-adapted external
// runtime sandboxing is active. It returns true only on Linux when
// DENOVA_CONTAINER_SANDBOX is set to "1" or "true" (case-insensitive).
func ContainerSandboxEnabled() bool {
	return containerSandboxEnabled(hostGOOS(), os.Getenv(EnvContainerSandbox))
}

func containerSandboxEnabled(goos, value string) bool {
	if goos != "linux" {
		return false
	}
	trimmed := strings.TrimSpace(value)
	return trimmed == "1" || strings.EqualFold(trimmed, "true")
}

// ManagedAgentCLIDir returns the cleaned directory for managed external agent
// CLI binaries. It is enabled only on Linux amd64/arm64 when
// DENOVA_MANAGED_CLI_DIR is a non-empty POSIX absolute path.
func ManagedAgentCLIDir() string {
	return managedAgentCLIDir(hostGOOS(), hostGOARCH(), os.Getenv(EnvManagedAgentCLIDir))
}

func managedAgentCLIDir(goos, goarch, rawDir string) string {
	if goos != "linux" {
		return ""
	}
	if goarch != "amd64" && goarch != "arm64" {
		return ""
	}
	dir := strings.TrimSpace(rawDir)
	if dir == "" || strings.ContainsRune(dir, 0) || !path.IsAbs(dir) {
		return ""
	}
	return path.Clean(dir)
}

// ManagedAgentCLIsEnabled reports whether Denova manages installation and
// updates for external agent CLI binaries in this environment.
func ManagedAgentCLIsEnabled() bool {
	return ManagedAgentCLIDir() != ""
}

// WebAuthEnabled reports whether headless web OAuth sign-in and sign-out for
// external agent runtimes are available in this environment.
func WebAuthEnabled() bool {
	return ManagedAgentCLIsEnabled() || ContainerSandboxEnabled()
}

// managedCLICandidates prepends the managed Linux CLI binary path when
// DENOVA_MANAGED_CLI_DIR is active so custom non-PATH managed directories take
// precedence over ambient host binaries.
func managedCLICandidates(environment []string, binaryName string, fallback ...string) []string {
	candidates := make([]string, 0, len(fallback)+1)
	raw, ok := lookupEnvironmentValue(environment, EnvManagedAgentCLIDir)
	if !ok {
		raw = os.Getenv(EnvManagedAgentCLIDir)
	}
	if dir := managedAgentCLIDir(hostGOOS(), hostGOARCH(), raw); dir != "" {
		candidates = append(candidates, path.Join(dir, binaryName))
	}
	return append(candidates, fallback...)
}

func lookupEnvironmentValue(environment []string, name string) (string, bool) {
	for index := len(environment) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(environment[index], "=")
		if ok && strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}
