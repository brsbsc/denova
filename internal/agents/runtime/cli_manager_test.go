package agentruntime

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"denova/config"
)

type tarFixtureEntry struct {
	Name     string
	Typeflag byte
	Body     []byte
	Linkname string
	Mode     int64
}

func buildTarGzFixture(t *testing.T, entries []tarFixtureEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typeflag := entry.Typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := entry.Mode
		if mode == 0 {
			if typeflag == tar.TypeDir {
				mode = 0o755
			} else {
				mode = 0o755
			}
		}
		hdr := &tar.Header{
			Name:     entry.Name,
			Mode:     mode,
			Typeflag: typeflag,
			Linkname: entry.Linkname,
			Size:     int64(len(entry.Body)),
		}
		if typeflag == tar.TypeDir || typeflag == tar.TypeSymlink || typeflag == tar.TypeLink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header %q: %v", entry.Name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write(entry.Body); err != nil {
				t.Fatalf("write tar body %q: %v", entry.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buf.Bytes()
}

func sha512Hex(data []byte) string {
	sum := sha512.Sum512(data)
	return hex.EncodeToString(sum[:])
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha512SRI(data []byte) string {
	sum := sha512.Sum512(data)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func waitForCLIStatus(t *testing.T, mgr *CLIManager, id config.RuntimeID, cond func(ManagedCLIStatus) bool) ManagedCLIStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := mgr.Status(id)
		if st != nil && cond(*st) {
			return *st
		}
		time.Sleep(5 * time.Millisecond)
	}
	st := mgr.Status(id)
	t.Fatalf("timed out waiting for %s CLI status condition; last=%+v", id, st)
	return ManagedCLIStatus{}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".denova-cli-") {
			t.Fatalf("stray temporary file left in %s: %s", dir, entry.Name())
		}
	}
}

func TestCLIManagerEnablementAndJSONContract(t *testing.T) {
	dir := t.TempDir()

	disabledCases := []struct {
		name string
		opts CLIManagerOptions
	}{
		{name: "empty dir", opts: CLIManagerOptions{Dir: "", GOOS: "linux", GOARCH: "amd64"}},
		{name: "relative dir", opts: CLIManagerOptions{Dir: "relative/bin", GOOS: "linux", GOARCH: "amd64"}},
		{name: "windows host", opts: CLIManagerOptions{Dir: dir, GOOS: "windows", GOARCH: "amd64"}},
		{name: "darwin host", opts: CLIManagerOptions{Dir: dir, GOOS: "darwin", GOARCH: "arm64"}},
		{name: "unsupported linux arch", opts: CLIManagerOptions{Dir: dir, GOOS: "linux", GOARCH: "386"}},
	}
	for _, tc := range disabledCases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewCLIManager(tc.opts)
			defer mgr.Close()
			if mgr.Enabled() {
				t.Fatalf("expected manager to be disabled for %+v", tc.opts)
			}
			if st := mgr.Status(config.RuntimeAntigravity); st != nil {
				t.Fatalf("expected nil status when disabled, got %+v", st)
			}
			if _, err := mgr.CheckUpdate(context.Background(), config.RuntimeCodex); !errors.Is(err, ErrCLIManagementUnsupported) {
				t.Fatalf("CheckUpdate error = %v, want ErrCLIManagementUnsupported", err)
			}
			if _, err := mgr.StartInstall(config.RuntimeClaude); !errors.Is(err, ErrCLIManagementUnsupported) {
				t.Fatalf("StartInstall error = %v, want ErrCLIManagementUnsupported", err)
			}
		})
	}

	mgr := NewCLIManager(CLIManagerOptions{Dir: dir, GOOS: "linux", GOARCH: "amd64"})
	defer mgr.Close()
	if !mgr.Enabled() {
		t.Fatal("expected manager to be enabled on linux/amd64 with absolute dir")
	}
	if st := mgr.Status(config.RuntimeNative); st != nil {
		t.Fatalf("expected nil status for native runtime, got %+v", st)
	}
	if st := mgr.Status(config.RuntimeGrok); st != nil {
		t.Fatalf("expected nil status for unmanaged grok runtime, got %+v", st)
	}
	if _, err := mgr.CheckUpdate(context.Background(), config.RuntimeGrok); !errors.Is(err, ErrCLIManagementUnsupported) {
		t.Fatalf("CheckUpdate(grok) = %v, want ErrCLIManagementUnsupported", err)
	}

	mgr.RecordInstalled(config.RuntimeClaude, true, "v2.1.320")
	mgr.SetPendingReload(config.RuntimeClaude, true)
	st := mgr.Status(config.RuntimeClaude)
	if st == nil || !st.Managed || !st.Installed || st.InstalledVersion != "2.1.320" || !st.PendingReload || st.State != "idle" {
		t.Fatalf("unexpected initial status: %+v", st)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal ManagedCLIStatus: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal ManagedCLIStatus: %v", err)
	}
	for _, requiredKey := range []string{"managed", "installed", "installed_version", "update_available", "state", "pending_reload"} {
		if _, ok := fields[requiredKey]; !ok {
			t.Fatalf("missing JSON field %q in %s", requiredKey, string(raw))
		}
	}
}

func TestCLISourcesAndInstallAllThreeProviders(t *testing.T) {
	t.Run("antigravity tar.gz amd64", func(t *testing.T) {
		dir := t.TempDir()
		binaryPayload := []byte("#!/bin/sh\necho 1.5.18\n")
		archive := buildTarGzFixture(t, []tarFixtureEntry{
			{Name: "README.txt", Body: []byte("readme")},
			{Name: "antigravity", Body: binaryPayload},
		})
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.18",
					"url":     srv.URL + "/downloads/antigravity-1.5.18-linux-amd64.tar.gz",
					"sha512":  sha512Hex(archive),
				})
			case "/downloads/antigravity-1.5.18-linux-amd64.tar.gz":
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(archive)))
				_, _ = w.Write(archive)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		var reloaded bool
		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			VerifyBinary: func(_ context.Context, id config.RuntimeID, path string) (string, error) {
				if id != config.RuntimeAntigravity {
					return "", fmt.Errorf("unexpected id %s", id)
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, binaryPayload) {
					return "", errors.New("staged binary content mismatch")
				}
				return parseVerifiedCLIVersion(id, "1.5.18\n")
			},
			OnReload: func(_ context.Context, id config.RuntimeID) (bool, string, error) {
				reloaded = id == config.RuntimeAntigravity
				return false, "1.5.18", nil
			},
		})
		defer mgr.Close()

		checked, err := mgr.CheckUpdate(context.Background(), config.RuntimeAntigravity)
		if err != nil {
			t.Fatalf("CheckUpdate: %v", err)
		}
		if checked.LatestVersion != "1.5.18" || checked.UpdateAvailable || checked.LastCheckedAt == "" {
			t.Fatalf("unexpected check result: %+v", checked)
		}

		started, err := mgr.StartInstall(config.RuntimeAntigravity)
		if err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		if started.State != "installing" {
			t.Fatalf("started state = %q, want installing", started.State)
		}

		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "idle" || s.State == "error"
		})
		if done.State != "idle" || !done.Installed || done.InstalledVersion != "1.5.18" || done.UpdateAvailable || !reloaded {
			t.Fatalf("unexpected final status: %+v (reloaded=%v)", done, reloaded)
		}
		installed, err := os.ReadFile(filepath.Join(dir, "agy"))
		if err != nil || !bytes.Equal(installed, binaryPayload) {
			t.Fatalf("installed agy mismatch: err=%v len=%d", err, len(installed))
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("antigravity direct binary arm64", func(t *testing.T) {
		dir := t.TempDir()
		binaryPayload := []byte("ELF-ANTIGRAVITY-ARM64-BINARY")
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_arm64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.20",
					"url":     srv.URL + "/downloads/antigravity-linux-arm64",
					"sha512":  sha512Hex(binaryPayload),
				})
			case "/downloads/antigravity-linux-arm64":
				_, _ = w.Write(binaryPayload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "arm64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
				return parseVerifiedCLIVersion(id, "1.5.20")
			},
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "idle" || s.State == "error"
		})
		if done.State != "idle" || done.InstalledVersion != "1.5.20" {
			t.Fatalf("unexpected direct install status: %+v", done)
		}
		got, err := os.ReadFile(filepath.Join(dir, "agy"))
		if err != nil || !bytes.Equal(got, binaryPayload) {
			t.Fatalf("direct agy binary mismatch: err=%v", err)
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("claude direct binary sha256 and size", func(t *testing.T) {
		dir := t.TempDir()
		binaryPayload := []byte("ELF-CLAUDE-CODE-LINUX-X64-BINARY")
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/claude-code-releases/latest":
				_, _ = w.Write([]byte("2.1.320\n"))
			case "/claude-code-releases/2.1.320/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.320",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex(binaryPayload),
							"size":     len(binaryPayload),
						},
					},
				})
			case "/claude-code-releases/2.1.320/linux-x64/claude":
				_, _ = w.Write(binaryPayload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeClaude: srv.URL + "/claude-code-releases"},
			VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
				return parseVerifiedCLIVersion(id, "2.1.320 (Claude Code)\n")
			},
		})
		defer mgr.Close()

		mgr.RecordInstalled(config.RuntimeClaude, true, "2.1.314")
		checked, err := mgr.CheckUpdate(context.Background(), config.RuntimeClaude)
		if err != nil {
			t.Fatalf("CheckUpdate: %v", err)
		}
		if !checked.UpdateAvailable || checked.LatestVersion != "2.1.320" {
			t.Fatalf("expected UpdateAvailable=true, got %+v", checked)
		}
		started, err := mgr.StartInstall(config.RuntimeClaude)
		if err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		if started.State != "updating" {
			t.Fatalf("expected updating state for installed CLI, got %q", started.State)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return s.State == "idle" || s.State == "error"
		})
		if done.State != "idle" || done.InstalledVersion != "2.1.320" || done.UpdateAvailable {
			t.Fatalf("unexpected final claude status: %+v", done)
		}
		got, err := os.ReadFile(filepath.Join(dir, "claude"))
		if err != nil || !bytes.Equal(got, binaryPayload) {
			t.Fatalf("installed claude mismatch: err=%v", err)
		}
		assertNoTempFiles(t, dir)
	})

	t.Run("codex npm sri tgz both vendor layouts", func(t *testing.T) {
		cases := []struct {
			name        string
			goarch      string
			pkgSuffix   string
			archivePath string
		}{
			{
				name:        "amd64 vendor codex/codex",
				goarch:      "amd64",
				pkgSuffix:   "linux-x64",
				archivePath: "package/vendor/x86_64-unknown-linux-musl/codex/codex",
			},
			{
				name:        "arm64 vendor bin/codex",
				goarch:      "arm64",
				pkgSuffix:   "linux-arm64",
				archivePath: "package/vendor/aarch64-unknown-linux-musl/bin/codex",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				binaryPayload := []byte("ELF-CODEX-" + tc.goarch)
				tgz := buildTarGzFixture(t, []tarFixtureEntry{
					{Name: "package/package.json", Body: []byte(`{"name":"@openai/codex"}`)},
					{Name: tc.archivePath, Body: binaryPayload},
				})
				var srv *httptest.Server
				srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/@openai/codex/latest":
						_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.135.0"})
					case "/@openai/codex/0.135.0-" + tc.pkgSuffix:
						_ = json.NewEncoder(w).Encode(map[string]any{
							"version": "0.135.0-" + tc.pkgSuffix,
							"dist": map[string]any{
								"tarball":   srv.URL + "/tarballs/codex.tgz",
								"integrity": sha512SRI(tgz),
							},
						})
					case "/tarballs/codex.tgz":
						_, _ = w.Write(tgz)
					default:
						http.NotFound(w, r)
					}
				}))
				defer srv.Close()

				mgr := NewCLIManager(CLIManagerOptions{
					Dir:            dir,
					GOOS:           "linux",
					GOARCH:         tc.goarch,
					HTTPClient:     srv.Client(),
					SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeCodex: srv.URL + "/@openai/codex"},
					VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
						return parseVerifiedCLIVersion(id, "codex-cli 0.135.0\n")
					},
				})
				defer mgr.Close()

				if _, err := mgr.StartInstall(config.RuntimeCodex); err != nil {
					t.Fatalf("StartInstall: %v", err)
				}
				done := waitForCLIStatus(t, mgr, config.RuntimeCodex, func(s ManagedCLIStatus) bool {
					return s.State == "idle" || s.State == "error"
				})
				if done.State != "idle" || done.InstalledVersion != "0.135.0" {
					t.Fatalf("unexpected codex status: %+v", done)
				}
				got, err := os.ReadFile(filepath.Join(dir, "codex"))
				if err != nil || !bytes.Equal(got, binaryPayload) {
					t.Fatalf("installed codex mismatch: err=%v", err)
				}
				if runtime.GOOS != "windows" {
					info, err := os.Stat(filepath.Join(dir, "codex"))
					if err != nil || info.Mode().Perm() != 0o755 {
						t.Fatalf("codex mode = %v, want 0755", info.Mode().Perm())
					}
				}
				assertNoTempFiles(t, dir)
			})
		}
	})
}

func TestCLISecurityRejectsMaliciousAndCorruptPackages(t *testing.T) {
	originalBytes := []byte("EXISTING-GOOD-BINARY-V1")

	setupExisting := func(t *testing.T, name string) (string, string) {
		t.Helper()
		dir := t.TempDir()
		target := filepath.Join(dir, name)
		if err := os.WriteFile(target, originalBytes, 0o755); err != nil {
			t.Fatalf("write existing binary: %v", err)
		}
		return dir, target
	}

	assertTargetIntact := func(t *testing.T, dir, target string) {
		t.Helper()
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("existing binary disappeared: %v", err)
		}
		if !bytes.Equal(got, originalBytes) {
			t.Fatalf("existing binary was corrupted: got %q, want %q", string(got), string(originalBytes))
		}
		assertNoTempFiles(t, dir)
	}

	t.Run("checksum mismatch", func(t *testing.T) {
		dir, target := setupExisting(t, "claude")
		payload := []byte("TAMPERED-CLAUDE-BINARY")
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest":
				_, _ = w.Write([]byte("2.1.325"))
			case "/2.1.325/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.325",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex([]byte("DIFFERENT-CONTENT")),
							"size":     len(payload),
						},
					},
				})
			case "/2.1.325/linux-x64/claude":
				_, _ = w.Write(payload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeClaude: srv.URL},
			VerifyBinary:   func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "2.1.325", nil },
		})
		defer mgr.Close()
		mgr.RecordInstalled(config.RuntimeClaude, true, "2.1.314")

		if _, err := mgr.StartInstall(config.RuntimeClaude); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || done.LastError == "" {
			t.Fatalf("expected error status on checksum mismatch, got %+v", done)
		}
		if done.InstalledVersion != "2.1.314" {
			t.Fatalf("installed version changed on failed update: %+v", done)
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("tar.gz path traversal rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "agy")
		badTar := buildTarGzFixture(t, []tarFixtureEntry{
			{Name: "antigravity", Body: []byte("GOOD")},
			{Name: "../evil", Body: []byte("PWNED")},
		})
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.19",
					"url":     srv.URL + "/bad.tar.gz",
					"sha512":  sha512Hex(badTar),
				})
			case "/bad.tar.gz":
				_, _ = w.Write(badTar)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			VerifyBinary:   func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "1.5.19", nil },
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || !strings.Contains(strings.ToLower(done.LastError), "path") {
			t.Fatalf("expected path traversal error, got %+v", done)
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("tar.gz symlink rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "codex")
		badTar := buildTarGzFixture(t, []tarFixtureEntry{
			{Name: "package/vendor/x86_64-unknown-linux-musl/codex/codex", Body: []byte("GOOD")},
			{Name: "package/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		})
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest":
				_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.136.0"})
			case "/0.136.0-linux-x64":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "0.136.0-linux-x64",
					"dist": map[string]any{
						"tarball":   srv.URL + "/bad.tgz",
						"integrity": sha512SRI(badTar),
					},
				})
			case "/bad.tgz":
				_, _ = w.Write(badTar)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeCodex: srv.URL},
			VerifyBinary:   func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "0.136.0", nil },
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeCodex); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeCodex, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || done.LastError == "" {
			t.Fatalf("expected symlink rejection error, got %+v", done)
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("extract size limit exceeded rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "agy")
		oversized := buildTarGzFixture(t, []tarFixtureEntry{
			{Name: "antigravity", Body: bytes.Repeat([]byte("A"), 4096)},
		})
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.19",
					"url":     srv.URL + "/oversized.tar.gz",
					"sha512":  sha512Hex(oversized),
				})
			case "/oversized.tar.gz":
				_, _ = w.Write(oversized)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:             dir,
			GOOS:            "linux",
			GOARCH:          "amd64",
			HTTPClient:      srv.Client(),
			SourceBaseURLs:  map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			MaxExtractBytes: 1024,
			VerifyBinary:    func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "1.5.19", nil },
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || !strings.Contains(strings.ToLower(done.LastError), "limit") {
			t.Fatalf("expected extract limit error, got %+v", done)
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("binary --version below minimum rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "agy")
		payload := []byte("OLD-ANTIGRAVITY-BINARY")
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.19",
					"url":     srv.URL + "/agy-bin",
					"sha512":  sha512Hex(payload),
				})
			case "/agy-bin":
				_, _ = w.Write(payload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
				// Simulate a binary claiming version 1.4.2 (< 1.4.4 minimum).
				return parseVerifiedCLIVersion(id, "1.4.2\n")
			},
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || done.LastError == "" {
			t.Fatalf("expected version rejection error, got %+v", done)
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("non-https download URL rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "agy")
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1.5.19",
				"url":     "http://insecure.example/agy.tar.gz",
				"sha512":  sha512Hex([]byte("x")),
			})
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
		})
		defer mgr.Close()

		if _, err := mgr.CheckUpdate(context.Background(), config.RuntimeAntigravity); err == nil {
			t.Fatal("expected error for non-https download URL")
		}
		assertTargetIntact(t, dir, target)
	})

	t.Run("tar.gz hardlink and download limit and https downgrade redirect rejected", func(t *testing.T) {
		dir, target := setupExisting(t, "agy")
		hardlinkTar := buildTarGzFixture(t, []tarFixtureEntry{
			{Name: "antigravity", Body: []byte("GOOD")},
			{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "antigravity"},
		})
		var srv *httptest.Server
		srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.21",
					"url":     srv.URL + "/hardlink.tar.gz",
					"sha512":  sha512Hex(hardlinkTar),
				})
			case "/hardlink.tar.gz":
				_, _ = w.Write(hardlinkTar)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: srv.URL},
			VerifyBinary:   func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "1.5.21", nil },
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if done.State != "error" || !strings.Contains(strings.ToLower(done.LastError), "link") {
			t.Fatalf("expected hardlink rejection error, got %+v", done)
		}
		assertTargetIntact(t, dir, target)

		// Download exceeding MaxDownloadBytes without Content-Length header.
		bigPayload := bytes.Repeat([]byte("X"), 2048)
		var bigSrv *httptest.Server
		bigSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifests/linux_amd64.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "1.5.21",
					"url":     bigSrv.URL + "/big-bin",
					"sha512":  sha512Hex(bigPayload),
				})
			case "/big-bin":
				flusher, _ := w.(http.Flusher)
				_, _ = w.Write(bigPayload[:1024])
				if flusher != nil {
					flusher.Flush()
				}
				_, _ = w.Write(bigPayload[1024:])
			default:
				http.NotFound(w, r)
			}
		}))
		defer bigSrv.Close()

		mgrCap := NewCLIManager(CLIManagerOptions{
			Dir:              dir,
			GOOS:             "linux",
			GOARCH:           "amd64",
			HTTPClient:       bigSrv.Client(),
			SourceBaseURLs:   map[config.RuntimeID]string{config.RuntimeAntigravity: bigSrv.URL},
			MaxDownloadBytes: 512,
			VerifyBinary:     func(_ context.Context, _ config.RuntimeID, _ string) (string, error) { return "1.5.21", nil },
		})
		defer mgrCap.Close()

		if _, err := mgrCap.StartInstall(config.RuntimeAntigravity); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		doneCap := waitForCLIStatus(t, mgrCap, config.RuntimeAntigravity, func(s ManagedCLIStatus) bool {
			return s.State == "error" || s.State == "idle"
		})
		if doneCap.State != "error" || !strings.Contains(strings.ToLower(doneCap.LastError), "limit") {
			t.Fatalf("expected download limit error, got %+v", doneCap)
		}
		assertTargetIntact(t, dir, target)

		// HTTPS -> HTTP redirect must be blocked.
		redirSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://insecure.example/manifests/linux_amd64.json", http.StatusFound)
		}))
		defer redirSrv.Close()

		mgrRedir := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     redirSrv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeAntigravity: redirSrv.URL},
		})
		defer mgrRedir.Close()
		if _, err := mgrRedir.CheckUpdate(context.Background(), config.RuntimeAntigravity); err == nil {
			t.Fatal("expected error when server redirects from https to http")
		}
	})
}

func TestCLIInstallProgressBusyLockAndPendingReload(t *testing.T) {
	dir := t.TempDir()
	chunk1 := bytes.Repeat([]byte("a"), 16*1024)
	chunk2 := bytes.Repeat([]byte("b"), 16*1024)
	fullPayload := append(append([]byte{}, chunk1...), chunk2...)

	firstChunkSent := make(chan struct{})
	releaseSecondChunk := make(chan struct{})
	var closeOnce sync.Once
	release := func() { closeOnce.Do(func() { close(releaseSecondChunk) }) }
	defer release()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			_, _ = w.Write([]byte("2.1.330"))
		case "/2.1.330/manifest.json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "2.1.330",
				"platforms": map[string]any{
					"linux-x64": map[string]any{
						"checksum": sha256Hex(fullPayload),
						"size":     len(fullPayload),
					},
				},
			})
		case "/2.1.330/linux-x64/claude":
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(fullPayload)))
			flusher, _ := w.(http.Flusher)
			_, _ = w.Write(chunk1)
			if flusher != nil {
				flusher.Flush()
			}
			close(firstChunkSent)
			select {
			case <-releaseSecondChunk:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(chunk2)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	mgr := NewCLIManager(CLIManagerOptions{
		Dir:            dir,
		GOOS:           "linux",
		GOARCH:         "amd64",
		HTTPClient:     srv.Client(),
		SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeClaude: srv.URL},
		VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
			return parseVerifiedCLIVersion(id, "2.1.330 (Claude Code)")
		},
		OnReload: func(_ context.Context, _ config.RuntimeID) (bool, string, error) {
			// Simulate active lease requiring pending reload.
			return true, "2.1.330", nil
		},
	})
	defer mgr.Close()

	if _, err := mgr.StartInstall(config.RuntimeClaude); err != nil {
		t.Fatalf("first StartInstall: %v", err)
	}
	if _, err := mgr.StartInstall(config.RuntimeClaude); !errors.Is(err, ErrCLIOperationBusy) {
		t.Fatalf("concurrent StartInstall = %v, want ErrCLIOperationBusy", err)
	}
	if _, err := mgr.CheckUpdate(context.Background(), config.RuntimeClaude); !errors.Is(err, ErrCLIOperationBusy) {
		t.Fatalf("concurrent CheckUpdate = %v, want ErrCLIOperationBusy", err)
	}

	<-firstChunkSent
	progress := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
		return s.BytesDownloaded >= int64(len(chunk1)) && s.TotalBytes == int64(len(fullPayload))
	})
	if progress.State != "installing" {
		t.Fatalf("mid-download state = %q, want installing", progress.State)
	}

	release()
	done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
		return s.State == "idle" || s.State == "error"
	})
	if done.State != "idle" || !done.PendingReload || done.InstalledVersion != "2.1.330" {
		t.Fatalf("unexpected final status with pending reload: %+v", done)
	}
	mgr.SetPendingReload(config.RuntimeClaude, false)
	if st := mgr.Status(config.RuntimeClaude); st.PendingReload {
		t.Fatalf("expected pending_reload cleared, got %+v", st)
	}
}

func TestCLICloseCancelsActiveDownloadAndRecoversPanic(t *testing.T) {
	t.Run("Close cancels in-flight install and cleans temp files", func(t *testing.T) {
		dir := t.TempDir()
		started := make(chan struct{})
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest":
				_, _ = w.Write([]byte("2.1.331"))
			case "/2.1.331/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.331",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex([]byte("PARTIAL")),
							"size":     1024,
						},
					},
				})
			case "/2.1.331/linux-x64/claude":
				flusher, _ := w.(http.Flusher)
				_, _ = w.Write([]byte("PART"))
				if flusher != nil {
					flusher.Flush()
				}
				close(started)
				<-r.Context().Done()
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeClaude: srv.URL},
		})
		if _, err := mgr.StartInstall(config.RuntimeClaude); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		<-started
		mgr.Close()
		assertNoTempFiles(t, dir)
	})

	t.Run("StartInstall recovers panic and releases busy lock", func(t *testing.T) {
		dir := t.TempDir()
		payload := []byte("PANIC-VERIFY-TEST")
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/latest":
				_, _ = w.Write([]byte("2.1.332"))
			case "/2.1.332/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.332",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex(payload),
							"size":     len(payload),
						},
					},
				})
			case "/2.1.332/linux-x64/claude":
				_, _ = w.Write(payload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:            dir,
			GOOS:           "linux",
			GOARCH:         "amd64",
			HTTPClient:     srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{config.RuntimeClaude: srv.URL},
			VerifyBinary: func(_ context.Context, _ config.RuntimeID, _ string) (string, error) {
				panic("simulated verifier panic")
			},
		})
		defer mgr.Close()

		if _, err := mgr.StartInstall(config.RuntimeClaude); err != nil {
			t.Fatalf("StartInstall: %v", err)
		}
		done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return s.State == "error"
		})
		if done.LastError == "" {
			t.Fatalf("expected non-empty LastError after recovered panic, got %+v", done)
		}
		assertNoTempFiles(t, dir)
	})
}

func TestCLIRunAutoUpdatePolicies(t *testing.T) {
	t.Run("updates only installed CLIs and respects enabled flag", func(t *testing.T) {
		dir := t.TempDir()
		claudePayload := []byte("CLAUDE-NEW-2.1.340")
		var claudeChecks, antigravityChecks, codexChecks atomic.Int32
		var autoEnabled atomic.Bool

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/claude/latest":
				claudeChecks.Add(1)
				_, _ = w.Write([]byte("2.1.340"))
			case "/claude/2.1.340/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.340",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex(claudePayload),
							"size":     len(claudePayload),
						},
					},
				})
			case "/claude/2.1.340/linux-x64/claude":
				_, _ = w.Write(claudePayload)
			case "/agy/manifests/linux_amd64.json":
				antigravityChecks.Add(1)
				http.NotFound(w, r)
			case "/codex/latest":
				codexChecks.Add(1)
				http.NotFound(w, r)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:        dir,
			GOOS:       "linux",
			GOARCH:     "amd64",
			HTTPClient: srv.Client(),
			SourceBaseURLs: map[config.RuntimeID]string{
				config.RuntimeAntigravity: srv.URL + "/agy",
				config.RuntimeClaude:      srv.URL + "/claude",
				config.RuntimeCodex:       srv.URL + "/codex",
			},
			VerifyBinary: func(_ context.Context, id config.RuntimeID, _ string) (string, error) {
				return parseVerifiedCLIVersion(id, "2.1.340 (Claude Code)")
			},
			AutoUpdateDelay:     5 * time.Millisecond,
			AutoUpdateTick:      10 * time.Millisecond,
			AutoUpdateInterval:  20 * time.Millisecond,
			AutoUpdateRetryWait: 10 * time.Millisecond,
		})
		defer mgr.Close()

		// Only Claude is installed; Antigravity and Codex are NOT installed.
		mgr.RecordInstalled(config.RuntimeClaude, true, "2.1.314")

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.RunAutoUpdate(ctx, func() (bool, error) { return autoEnabled.Load(), nil })
		}()

		// While disabled, no checks or installs should occur.
		time.Sleep(35 * time.Millisecond)
		if claudeChecks.Load() != 0 || antigravityChecks.Load() != 0 || codexChecks.Load() != 0 {
			t.Fatalf("auto-update ran while disabled: claude=%d agy=%d codex=%d", claudeChecks.Load(), antigravityChecks.Load(), codexChecks.Load())
		}

		// Enable auto-update and wait for Claude to update.
		autoEnabled.Store(true)
		done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return s.InstalledVersion == "2.1.340" && s.State == "idle"
		})
		if done.UpdateAvailable {
			t.Fatalf("expected UpdateAvailable=false after auto-update, got %+v", done)
		}
		if antigravityChecks.Load() != 0 || codexChecks.Load() != 0 {
			t.Fatalf("uninstalled CLIs were checked by auto-update: agy=%d codex=%d", antigravityChecks.Load(), codexChecks.Load())
		}

		cancel()
		wg.Wait()
	})

	t.Run("does not retry same failed version in a loop", func(t *testing.T) {
		dir := t.TempDir()
		var downloadAttempts atomic.Int32
		var latestVer atomic.Value
		latestVer.Store("2.1.350")
		goodPayload := []byte("CLAUDE-GOOD-2.1.351")

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ver := latestVer.Load().(string)
			switch r.URL.Path {
			case "/latest":
				_, _ = w.Write([]byte(ver))
			case "/2.1.350/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.350",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex([]byte("EXPECTED")),
							"size":     len("CORRUPT!"),
						},
					},
				})
			case "/2.1.350/linux-x64/claude":
				downloadAttempts.Add(1)
				_, _ = w.Write([]byte("CORRUPT!"))
			case "/2.1.351/manifest.json":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"version": "2.1.351",
					"platforms": map[string]any{
						"linux-x64": map[string]any{
							"checksum": sha256Hex(goodPayload),
							"size":     len(goodPayload),
						},
					},
				})
			case "/2.1.351/linux-x64/claude":
				downloadAttempts.Add(1)
				_, _ = w.Write(goodPayload)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()

		mgr := NewCLIManager(CLIManagerOptions{
			Dir:                 dir,
			GOOS:                "linux",
			GOARCH:              "amd64",
			HTTPClient:          srv.Client(),
			SourceBaseURLs:      map[config.RuntimeID]string{config.RuntimeClaude: srv.URL},
			VerifyBinary:        func(_ context.Context, id config.RuntimeID, _ string) (string, error) { return "2.1.351", nil },
			AutoUpdateDelay:     5 * time.Millisecond,
			AutoUpdateTick:      10 * time.Millisecond,
			AutoUpdateInterval:  15 * time.Millisecond,
			AutoUpdateRetryWait: 10 * time.Millisecond,
		})
		defer mgr.Close()
		mgr.RecordInstalled(config.RuntimeClaude, true, "2.1.314")

		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			mgr.RunAutoUpdate(ctx, func() (bool, error) { return true, nil })
		}()

		// Wait until first failed download attempt finishes.
		waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return downloadAttempts.Load() >= 1 && s.State == "error"
		})
		// Let several ticks + check intervals + retry waits pass.
		time.Sleep(80 * time.Millisecond)
		if got := downloadAttempts.Load(); got != 1 {
			t.Fatalf("failed version 2.1.350 was retried automatically: attempts=%d, want 1", got)
		}

		// Once upstream publishes a newer version, auto-update should try and succeed.
		latestVer.Store("2.1.351")
		done := waitForCLIStatus(t, mgr, config.RuntimeClaude, func(s ManagedCLIStatus) bool {
			return s.InstalledVersion == "2.1.351" && s.State == "idle"
		})
		if done.InstalledVersion != "2.1.351" || downloadAttempts.Load() != 2 {
			t.Fatalf("expected newer version to succeed: status=%+v attempts=%d", done, downloadAttempts.Load())
		}

		cancel()
		wg.Wait()
	})
}

func TestCLIParseVerifiedVersion(t *testing.T) {
	valid := []struct {
		id     config.RuntimeID
		output string
		want   string
	}{
		{config.RuntimeAntigravity, "1.4.4\n", "1.4.4"},
		{config.RuntimeAntigravity, "1.5.18", "1.5.18"},
		{config.RuntimeClaude, "2.1.314 (Claude Code)\n", "2.1.314"},
		{config.RuntimeCodex, "codex-cli 0.130.0\n", "0.130.0"},
		{config.RuntimeCodex, "codex-cli v0.135.2", "0.135.2"},
	}
	for _, tc := range valid {
		got, err := parseVerifiedCLIVersion(tc.id, tc.output)
		if err != nil || got != tc.want {
			t.Fatalf("parseVerifiedCLIVersion(%s, %q) = (%q, %v), want (%q, nil)", tc.id, tc.output, got, err, tc.want)
		}
	}

	invalid := []struct {
		id     config.RuntimeID
		output string
	}{
		{config.RuntimeAntigravity, "1.4.2"},
		{config.RuntimeAntigravity, "not-a-version"},
		{config.RuntimeClaude, "2.1.313 (Claude Code)"},
		{config.RuntimeClaude, ""},
		{config.RuntimeCodex, "0.135.0"},
		{config.RuntimeCodex, "codex-cli 0.129.9"},
	}
	for _, tc := range invalid {
		if got, err := parseVerifiedCLIVersion(tc.id, tc.output); err == nil {
			t.Fatalf("parseVerifiedCLIVersion(%s, %q) unexpectedly succeeded with %q", tc.id, tc.output, got)
		}
	}
}
