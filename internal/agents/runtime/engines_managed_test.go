package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"denova/config"
	"denova/internal/agents/runtime/external"
)

type versionedTestConnection struct {
	testConnection
	version string
}

func (c *versionedTestConnection) Version() string { return c.version }

type staticCLISource struct {
	release CLIRelease
}

func (s staticCLISource) Resolve(context.Context, *http.Client, string) (CLIRelease, error) {
	return s.release, nil
}

func TestEnginesManagedNonContainerCatalogOmitsManagedAndAuth(t *testing.T) {
	engines := NewEnginesWithOptions(EnginesOptions{
		CLIManager:  &CLIManagerOptions{Dir: "", GOOS: "windows", GOARCH: "amd64"},
		AuthManager: &AuthManagerOptions{Enabled: func() bool { return false }},
	})
	defer engines.Close()

	if engines.CLI().Enabled() {
		t.Fatal("expected CLI manager disabled in non-managed mode")
	}
	for _, id := range []config.RuntimeID{config.RuntimeNative, config.RuntimeCodex, config.RuntimeClaude, config.RuntimeGrok, config.RuntimeAntigravity} {
		if engines.Auth().Supported(id) {
			t.Fatalf("Auth().Supported(%q) = true in non-container mode", id)
		}
	}

	catalog := engines.Catalog()
	if len(catalog) != 5 {
		t.Fatalf("catalog len = %d, want 5", len(catalog))
	}
	for _, item := range catalog {
		if item.AuthSupported || item.Managed != nil {
			t.Fatalf("non-managed descriptor %q leaked managed/auth fields: %+v", item.ID, item)
		}
		raw, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("marshal %q: %v", item.ID, err)
		}
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatalf("unmarshal %q: %v", item.ID, err)
		}
		if _, exists := obj["managed"]; exists {
			t.Fatalf("non-managed descriptor %q JSON contained managed: %s", item.ID, string(raw))
		}
		if _, exists := obj["auth_supported"]; exists {
			t.Fatalf("non-managed descriptor %q JSON contained auth_supported: %s", item.ID, string(raw))
		}
	}

	if _, err := engines.Descriptor(config.RuntimeID("unknown")); !errors.Is(err, ErrEngineNotFound) {
		t.Fatalf("Descriptor(unknown) = %v, want ErrEngineNotFound", err)
	}
	nativeDesc, err := engines.Descriptor(config.RuntimeNative)
	if err != nil || nativeDesc.Status != "ready" {
		t.Fatalf("Descriptor(native) = (%+v, %v)", nativeDesc, err)
	}
}

func TestEnginesManagedCheckSyncsVersionAndInstalledState(t *testing.T) {
	cliDir := t.TempDir()
	var (
		factoryMu    sync.Mutex
		factoryCalls atomic.Int32
		nextConn     external.Connection
		nextErr      error = ErrEngineNotInstalled
	)
	engines := NewEnginesWithOptions(EnginesOptions{
		CLIManager: &CLIManagerOptions{
			Dir:    cliDir,
			GOOS:   "linux",
			GOARCH: "amd64",
		},
		AuthManager: &AuthManagerOptions{
			Enabled: func() bool { return true },
		},
		Factories: map[config.RuntimeID]func(context.Context) (external.Connection, error){
			config.RuntimeCodex: func(context.Context) (external.Connection, error) {
				factoryCalls.Add(1)
				factoryMu.Lock()
				defer factoryMu.Unlock()
				return nextConn, nextErr
			},
		},
	})
	defer engines.Close()

	// Catalog must never spawn processes and must expose managed/auth flags only on supported runtimes.
	catalog := engines.Catalog()
	if factoryCalls.Load() != 0 {
		t.Fatalf("Catalog spawned factory %d times", factoryCalls.Load())
	}
	byID := make(map[config.RuntimeID]EngineDescriptor, len(catalog))
	for _, item := range catalog {
		byID[item.ID] = item
	}
	for _, id := range []config.RuntimeID{config.RuntimeCodex, config.RuntimeClaude, config.RuntimeAntigravity} {
		desc := byID[id]
		if !desc.AuthSupported || desc.Managed == nil || !desc.Managed.Managed || desc.Managed.Installed {
			t.Fatalf("initial managed descriptor %q = %+v", id, desc)
		}
	}
	for _, id := range []config.RuntimeID{config.RuntimeNative, config.RuntimeGrok} {
		desc := byID[id]
		if desc.AuthSupported || desc.Managed != nil {
			t.Fatalf("unmanaged runtime %q unexpectedly exposed managed/auth: %+v", id, desc)
		}
	}

	// 1. Not installed -> CLIManager records installed=false.
	checked, err := engines.Check(context.Background(), config.RuntimeCodex)
	if err != nil || checked.Status != "not_installed" || checked.Version != "" {
		t.Fatalf("not_installed Check = (%+v, %v)", checked, err)
	}
	if checked.Managed == nil || checked.Managed.Installed || checked.Managed.InstalledVersion != "" {
		t.Fatalf("not_installed Managed = %+v", checked.Managed)
	}

	// 2. Installed but auth_required -> CLIManager MUST record installed=true and version.
	factoryMu.Lock()
	nextConn = &versionedTestConnection{
		testConnection: testConnection{state: external.ConnectionState{Status: "auth_required"}},
		version:        "0.131.0",
	}
	nextErr = nil
	factoryMu.Unlock()

	checked, err = engines.Check(context.Background(), config.RuntimeCodex)
	if err != nil || checked.Status != "auth_required" || checked.Version != "0.131.0" {
		t.Fatalf("auth_required Check = (%+v, %v)", checked, err)
	}
	if checked.Managed == nil || !checked.Managed.Installed || checked.Managed.InstalledVersion != "0.131.0" {
		t.Fatalf("auth_required must record installed binary: %+v", checked.Managed)
	}

	// 3. Ready -> CLIManager records installed=true and updated version.
	factoryMu.Lock()
	nextConn = &versionedTestConnection{
		testConnection: testConnection{state: external.ConnectionState{Status: "ready"}},
		version:        "0.132.0",
	}
	factoryMu.Unlock()

	checked, err = engines.Check(context.Background(), config.RuntimeCodex)
	if err != nil || checked.Status != "ready" || checked.Version != "0.132.0" {
		t.Fatalf("ready Check = (%+v, %v)", checked, err)
	}
	if checked.Managed == nil || !checked.Managed.Installed || checked.Managed.InstalledVersion != "0.132.0" {
		t.Fatalf("ready Managed = %+v", checked.Managed)
	}

	// 4. Binary removed -> Check clears installed state and version.
	factoryMu.Lock()
	nextConn = nil
	nextErr = ErrEngineNotInstalled
	factoryMu.Unlock()

	checked, err = engines.Check(context.Background(), config.RuntimeCodex)
	if err != nil || checked.Status != "not_installed" || checked.Version != "" {
		t.Fatalf("removed Check = (%+v, %v)", checked, err)
	}
	if checked.Managed == nil || checked.Managed.Installed || checked.Managed.InstalledVersion != "" {
		t.Fatalf("removed Managed = %+v", checked.Managed)
	}
}

// TestEnginesManagedReloadBinaryDeferredWhileLeasedAndLogoutBlocked covers
// Review Focus #3: active conversation leases prevent closing connections during
// hot binary replacement and reject concurrent logout with ErrOperationActive.
func TestEnginesManagedReloadBinaryDeferredWhileLeasedAndLogoutBlocked(t *testing.T) {
	cliDir := t.TempDir()
	connV1 := &versionedTestConnection{
		testConnection: testConnection{state: external.ConnectionState{Status: "ready"}},
		version:        "0.130.0",
	}
	connV2 := &versionedTestConnection{
		testConnection: testConnection{state: external.ConnectionState{Status: "ready"}},
		version:        "0.131.0",
	}
	connV3 := &versionedTestConnection{
		testConnection: testConnection{state: external.ConnectionState{Status: "auth_required"}},
		version:        "0.132.0",
	}

	var (
		factoryMu   sync.Mutex
		currentConn external.Connection = connV1
		logoutCalls atomic.Int32
	)

	payload := []byte("#!/bin/sh\necho codex-cli 0.131.0\n")
	digest := sha256.Sum256(payload)
	dlServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer dlServer.Close()

	engines := NewEnginesWithOptions(EnginesOptions{
		CLIManager: &CLIManagerOptions{
			Dir:        cliDir,
			GOOS:       "linux",
			GOARCH:     "amd64",
			HTTPClient: dlServer.Client(),
			Sources: map[config.RuntimeID]CLISource{
				config.RuntimeCodex: staticCLISource{
					release: CLIRelease{
						Version:      "0.131.0",
						DownloadURL:  dlServer.URL + "/codex",
						HashAlgo:     "sha256",
						Digest:       digest[:],
						ExpectedSize: int64(len(payload)),
					},
				},
			},
			VerifyBinary: func(_ context.Context, _ config.RuntimeID, _ string) (string, error) {
				return "0.131.0", nil
			},
		},
		AuthManager: &AuthManagerOptions{
			Enabled: func() bool { return true },
			Discover: func(config.RuntimeID) (string, []string, []string, error) {
				logoutCalls.Add(1)
				return "", nil, nil, ErrEngineNotInstalled
			},
		},
		Factories: map[config.RuntimeID]func(context.Context) (external.Connection, error){
			config.RuntimeCodex: func(context.Context) (external.Connection, error) {
				factoryMu.Lock()
				defer factoryMu.Unlock()
				return currentConn, nil
			},
		},
	})
	defer engines.Close()

	ctx := context.Background()
	selection := config.RuntimeSelection{
		Kind:  config.RuntimeCodex,
		Codex: &config.CodexRuntimeSettings{Model: "runtime-model", Effort: "high"},
	}

	// Acquire an active conversation lease on connV1.
	adapter, release, err := engines.Acquire(ctx, selection, config.Config{})
	if err != nil || adapter != connV1 {
		t.Fatalf("initial Acquire = (%v, %v)", adapter, err)
	}

	// Logout while active MUST fail with ErrOperationActive and touch nothing.
	if _, err := engines.Auth().Logout(ctx, config.RuntimeCodex); !errors.Is(err, ErrOperationActive) {
		t.Fatalf("Logout while active = %v, want ErrOperationActive", err)
	}
	if logoutCalls.Load() != 0 || connV1.closes != 0 {
		t.Fatalf("active Logout ran CLI or closed connection: logoutCalls=%d closes=%d", logoutCalls.Load(), connV1.closes)
	}

	// Switch factory to connV2 and run a full managed CLI install while connV1 is leased.
	factoryMu.Lock()
	currentConn = connV2
	factoryMu.Unlock()

	if _, err := engines.CLI().StartInstall(config.RuntimeCodex); err != nil {
		t.Fatalf("StartInstall failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := engines.CLI().Status(config.RuntimeCodex)
		if st != nil && st.State == "idle" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Disk binary must be replaced atomically, connV1 MUST remain open, and pending_reload MUST be true.
	if _, err := os.Stat(filepath.Join(cliDir, "codex")); err != nil {
		t.Fatalf("expected replaced codex binary on disk: %v", err)
	}
	if connV1.closes != 0 {
		t.Fatalf("ReloadBinary closed active leased connection: closes=%d", connV1.closes)
	}
	desc, err := engines.Descriptor(config.RuntimeCodex)
	if err != nil || desc.Managed == nil {
		t.Fatalf("Descriptor after leased install = (%+v, %v)", desc, err)
	}
	if !desc.Managed.PendingReload || desc.Managed.InstalledVersion != "0.131.0" || desc.Managed.State != "idle" {
		t.Fatalf("expected pending_reload=true and installed_version=0.131.0, got %+v", desc.Managed)
	}

	// Releasing the lease must close connV1 and clear pending_reload.
	release()
	if connV1.closes != 1 {
		t.Fatalf("lease release did not close stale connection: closes=%d", connV1.closes)
	}
	desc, err = engines.Descriptor(config.RuntimeCodex)
	if err != nil || desc.Managed == nil || desc.Managed.PendingReload {
		t.Fatalf("pending_reload was not cleared on lease release: %+v", desc.Managed)
	}

	// Next Acquire seamlessly picks up connV2.
	adapter2, release2, err := engines.Acquire(ctx, selection, config.Config{})
	if err != nil || adapter2 != connV2 {
		t.Fatalf("post-reload Acquire = (%v, %v), want connV2", adapter2, err)
	}
	if desc, err := engines.Descriptor(config.RuntimeCodex); err != nil || desc.Version != "0.131.0" {
		t.Fatalf("Descriptor after reload = (%+v, %v)", desc, err)
	}
	release2()

	// Idle ReloadBinary recycles connV2 immediately and reflects auth_required + new version.
	factoryMu.Lock()
	currentConn = connV3
	factoryMu.Unlock()
	pending, reloadedVer, err := engines.ReloadBinary(ctx, config.RuntimeCodex)
	if err != nil || pending || reloadedVer != "0.132.0" {
		t.Fatalf("idle ReloadBinary = (pending=%v, ver=%q, err=%v)", pending, reloadedVer, err)
	}
	if connV2.closes != 1 {
		t.Fatalf("idle ReloadBinary did not close connV2: closes=%d", connV2.closes)
	}
	desc, err = engines.Descriptor(config.RuntimeCodex)
	if err != nil || desc.Status != "auth_required" || desc.Version != "0.132.0" || desc.Managed == nil || !desc.Managed.Installed || desc.Managed.InstalledVersion != "0.132.0" || desc.Managed.PendingReload {
		t.Fatalf("unexpected descriptor after idle reload: %+v (err=%v)", desc, err)
	}
}

func TestEnginesManagedCloseReapsAuthSessions(t *testing.T) {
	engines := NewEnginesWithOptions(EnginesOptions{
		CLIManager: &CLIManagerOptions{Dir: t.TempDir(), GOOS: "linux", GOARCH: "amd64"},
		AuthManager: &AuthManagerOptions{
			Enabled:     func() bool { return true },
			Discover:    helperDiscover(t, "codex_block"),
			PreferPipes: true,
		},
	})

	snap, err := engines.Auth().Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForAuthStatus(t, engines.Auth(), config.RuntimeCodex, snap.SessionID, AuthStatusAwaitingUser)

	done := make(chan error, 1)
	go func() { done <- engines.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Engines.Close failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Engines.Close blocked waiting for auth subprocess")
	}
}
