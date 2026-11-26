package agentruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"denova/config"
)

const authTestHelperEnv = "DENOVA_AUTH_HELPER_MODE"

// TestAuthHelperProcess acts as a fake CLI subprocess for Codex, Antigravity,
// and Claude interactive login/logout flows. It does nothing during normal test
// runs unless DENOVA_AUTH_HELPER_MODE is set.
func TestAuthHelperProcess(t *testing.T) {
	mode := os.Getenv(authTestHelperEnv)
	if mode == "" {
		return
	}
	args := helperArgsAfterSeparator(os.Args)
	switch mode {
	case "codex_device":
		if strings.Join(args, " ") != "login --device-auth" {
			fmt.Fprintf(os.Stderr, "unexpected args: %v\n", args)
			os.Exit(2)
		}
		if os.Getenv("BROWSER") != "false" || os.Getenv("NO_BROWSER") != "1" {
			fmt.Fprintln(os.Stderr, "missing browser disable env")
			os.Exit(3)
		}
		fmt.Print("RAW_PTY_SECRET_SENTINEL_CODEX\n")
		fmt.Print("\x1b[36mOpen https://auth.openai.com/codex/device\x1b[0m\n")
		fmt.Print("Enter one-time code: \x1b[1;32mABCD-1234\x1b[0m\n")
		waitForMarkerOrSleep(os.Getenv("DENOVA_AUTH_RELEASE_FILE"), 150*time.Millisecond)
		os.Exit(0)

	case "codex_block":
		writePIDFile(os.Getenv("DENOVA_AUTH_PID_FILE"))
		fmt.Print("\x1b[36mhttps://auth.openai.com/codex/device\x1b[0m\nCode: WXYZ-9876\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)

	case "codex_logout":
		if strings.Join(args, " ") != "logout" {
			os.Exit(2)
		}
		if marker := os.Getenv("DENOVA_AUTH_LOGOUT_FILE"); marker != "" {
			_ = os.WriteFile(marker, []byte("codex-logged-out"), 0o600)
		}
		os.Exit(0)

	case "antigravity_code":
		if strings.Join(args, " ") != "acp --login" {
			fmt.Fprintf(os.Stderr, "unexpected args: %v\n", args)
			os.Exit(2)
		}
		if os.Getenv("SSH_CONNECTION") != "denova 0 denova 0" || os.Getenv("BROWSER") != "false" || os.Getenv("TERM") != "xterm-256color" {
			fmt.Fprintln(os.Stderr, "missing headless PTY env")
			os.Exit(3)
		}
		port := os.Getenv("DENOVA_AUTH_REDIRECT_PORT")
		if port == "" {
			port = "43210"
		}
		fmt.Printf("RAW_PTY_SECRET_SENTINEL_AGY\n\x1b[34mhttps://accounts.google.com/o/oauth2/v2/auth?client_id=test-agy&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A%s%%2Fcallback&state=agy-state\x1b[0m\nPaste code: ", port)
		line := readHelperInputLine(os.Stdin)
		if line != os.Getenv("DENOVA_AUTH_EXPECT_CODE") {
			fmt.Fprintf(os.Stderr, "unexpected code\n")
			os.Exit(4)
		}
		os.Exit(0)

	case "claude_code":
		wantMethod := os.Getenv("DENOVA_AUTH_EXPECT_METHOD")
		if wantMethod == "" {
			wantMethod = "--claudeai"
		}
		if strings.Join(args, " ") != "auth login "+wantMethod {
			fmt.Fprintf(os.Stderr, "unexpected args: %v\n", args)
			os.Exit(2)
		}
		if os.Getenv("SSH_CONNECTION") != "denova 0 denova 0" || os.Getenv("BROWSER") != "false" || os.Getenv("TERM") != "xterm-256color" {
			fmt.Fprintln(os.Stderr, "missing headless PTY env")
			os.Exit(3)
		}
		port := os.Getenv("DENOVA_AUTH_REDIRECT_PORT")
		if port == "" {
			port = "54321"
		}
		fmt.Printf("RAW_PTY_SECRET_SENTINEL_CLAUDE\nVisit \x1b[4mhttps://claude.ai/oauth/authorize?client_id=test-claude&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A%s%%2Fcallback&state=claude-state\x1b[0m\nCode: ", port)
		line := readHelperInputLine(os.Stdin)
		if line != os.Getenv("DENOVA_AUTH_EXPECT_CODE") {
			os.Exit(4)
		}
		os.Exit(0)

	case "claude_logout":
		if strings.Join(args, " ") != "auth logout" {
			os.Exit(2)
		}
		if marker := os.Getenv("DENOVA_AUTH_LOGOUT_FILE"); marker != "" {
			_ = os.WriteFile(marker, []byte("claude-logged-out"), 0o600)
		}
		os.Exit(0)

	case "callback_wait":
		port := os.Getenv("DENOVA_AUTH_REDIRECT_PORT")
		fmt.Printf("RAW_PTY_SECRET_SENTINEL_CB\nhttps://accounts.google.com/o/oauth2/v2/auth?client_id=cb&redirect_uri=http%%3A%%2F%%2Flocalhost%%3A%s%%2Fcallback&state=cb-state\n", port)
		waitForMarkerOrSleep(os.Getenv("DENOVA_AUTH_RELEASE_FILE"), 10*time.Second)
		os.Exit(0)

	case "claude_banner_then_oauth":
		fmt.Print("Welcome to Claude Code: https://claude.ai\n")
		time.Sleep(220 * time.Millisecond)
		fmt.Print("Open https://claude.com/cai/oauth/authorize?client_id=banner&redirect_uri=http%3A%2F%2Flocalhost%3A48777%2Fcallback&state=s2\nCode: ")
		line := readHelperInputLine(os.Stdin)
		if line != os.Getenv("DENOVA_AUTH_EXPECT_CODE") {
			os.Exit(4)
		}
		os.Exit(0)

	case "exit_fail":
		fmt.Print("https://auth.openai.com/codex/device\nCode: FAIL-0001\n")
		os.Exit(1)

	default:
		os.Exit(99)
	}
}

func helperArgsAfterSeparator(all []string) []string {
	for i, arg := range all {
		if arg == "--" {
			return all[i+1:]
		}
	}
	return nil
}

func readHelperInputLine(r io.Reader) string {
	reader := bufio.NewReader(r)
	var out strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			break
		}
		if b == '\n' || b == '\r' {
			break
		}
		out.WriteByte(b)
	}
	return out.String()
}

func waitForMarkerOrSleep(markerPath string, fallback time.Duration) {
	if markerPath == "" {
		time.Sleep(fallback)
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerPath); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writePIDFile(path string) {
	if path == "" {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func helperDiscover(t *testing.T, mode string, extraEnv ...string) func(config.RuntimeID) (string, []string, []string, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	return func(config.RuntimeID) (string, []string, []string, error) {
		env := append([]string{authTestHelperEnv + "=" + mode}, extraEnv...)
		return exe, []string{"-test.run=^TestAuthHelperProcess$", "--"}, env, nil
	}
}

func waitForAuthStatus(t *testing.T, mgr *AuthManager, engine config.RuntimeID, sessionID string, want string) AuthSessionSnapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap, err := mgr.Get(engine, sessionID)
		if err == nil && snap.Status == want {
			return snap
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, err := mgr.Get(engine, sessionID)
	t.Fatalf("timed out waiting for auth status %q: last=(%+v, err=%v)", want, snap, err)
	return AuthSessionSnapshot{}
}

type capturedLogEntry struct {
	Message string
	Attrs   string
}

type logCaptureHandler struct {
	mu      sync.Mutex
	entries []capturedLogEntry
	prefix  string
}

func (h *logCaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logCaptureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(h.prefix)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
		return true
	})
	h.mu.Lock()
	h.entries = append(h.entries, capturedLogEntry{Message: r.Message, Attrs: b.String()})
	h.mu.Unlock()
	return nil
}

func (h *logCaptureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.prefix)
	for _, a := range attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value.Any())
	}
	return &logCaptureHandler{prefix: b.String()}
}

func (h *logCaptureHandler) WithGroup(string) slog.Handler { return h }

func (h *logCaptureHandler) dump() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, e := range h.entries {
		b.WriteString(e.Message)
		b.WriteString(" ")
		b.WriteString(e.Attrs)
		b.WriteString("\n")
	}
	return b.String()
}

func TestAuthCodexDeviceCodeFlowAndJSONSnapshot(t *testing.T) {
	releaseFile := filepath.Join(t.TempDir(), "release")
	var probeCalls atomic.Int32
	mgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Discover: helperDiscover(t, "codex_device", "DENOVA_AUTH_RELEASE_FILE="+releaseFile),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			probeCalls.Add(1)
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	defer mgr.Close()

	if !mgr.Supported(config.RuntimeCodex) || mgr.Supported(config.RuntimeNative) || mgr.Supported(config.RuntimeGrok) {
		t.Fatalf("unexpected Supported values")
	}
	if _, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{Method: "console"}); !errors.Is(err, ErrAuthInputInvalid) {
		t.Fatalf("expected ErrAuthInputInvalid for non-empty Codex method, got %v", err)
	}

	started, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if started.Mode != AuthModeDeviceCode || started.Engine != config.RuntimeCodex || started.SessionID == "" {
		t.Fatalf("unexpected initial snapshot: %+v", started)
	}

	awaiting := waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusAwaitingUser)
	if awaiting.AuthURL != "https://auth.openai.com/codex/device" {
		t.Fatalf("AuthURL = %q, want https://auth.openai.com/codex/device", awaiting.AuthURL)
	}
	if awaiting.UserCode != "ABCD-1234" {
		t.Fatalf("UserCode = %q, want ABCD-1234", awaiting.UserCode)
	}
	if awaiting.HintKey != "agentRuntime.auth.codexDeviceHint" {
		t.Fatalf("HintKey = %q, want agentRuntime.auth.codexDeviceHint", awaiting.HintKey)
	}

	// Device-code sessions must reject Submit.
	if _, err := mgr.Submit(context.Background(), config.RuntimeCodex, started.SessionID, "anything"); !errors.Is(err, ErrAuthSessionInvalidState) {
		t.Fatalf("expected ErrAuthSessionInvalidState on device_code Submit, got %v", err)
	}

	// Verify JSON field names match contract.
	rawJSON, err := json.Marshal(awaiting)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(rawJSON, &fields); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	for _, key := range []string{"session_id", "engine", "mode", "status", "auth_url", "user_code", "hint_key", "expires_at"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("missing JSON field %q in %s", key, string(rawJSON))
		}
	}

	// Allow fake codex CLI to exit 0 and trigger Probe.
	if err := os.WriteFile(releaseFile, []byte("ok"), 0o600); err != nil {
		t.Fatalf("write release file: %v", err)
	}
	succeeded := waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusSucceeded)
	if probeCalls.Load() != 1 {
		t.Fatalf("Probe calls = %d, want 1", probeCalls.Load())
	}
	if succeeded.EngineState == nil || succeeded.EngineState.Status != "ready" {
		t.Fatalf("expected ready EngineState, got %+v", succeeded.EngineState)
	}
}

func TestAuthCodePasteAntigravityAndClaudeFlows(t *testing.T) {
	tests := []struct {
		name         string
		engine       config.RuntimeID
		mode         string
		method       string
		expectMethod string
		wantURLPref  string
		code         string
	}{
		{
			name:        "antigravity_code_paste",
			engine:      config.RuntimeAntigravity,
			mode:        "antigravity_code",
			wantURLPref: "https://accounts.google.com/o/oauth2/v2/auth?",
			code:        "4/0A-agy-auth-code",
		},
		{
			name:         "claude_default_claudeai",
			engine:       config.RuntimeClaude,
			mode:         "claude_code",
			method:       "",
			expectMethod: "--claudeai",
			wantURLPref:  "https://claude.ai/oauth/authorize?",
			code:         "claude-code#state123",
		},
		{
			name:         "claude_console_method",
			engine:       config.RuntimeClaude,
			mode:         "claude_code",
			method:       "console",
			expectMethod: "--console",
			wantURLPref:  "https://claude.ai/oauth/authorize?",
			code:         "console-code#state456",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewAuthManager(AuthManagerOptions{
				Enabled: func() bool { return true },
				Discover: helperDiscover(
					t,
					tc.mode,
					"DENOVA_AUTH_EXPECT_CODE="+tc.code,
					"DENOVA_AUTH_EXPECT_METHOD="+tc.expectMethod,
				),
				Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
					desc := engineDescriptor(id)
					desc.Status = "ready"
					return desc, nil
				},
			})
			defer mgr.Close()

			started, err := mgr.Start(context.Background(), tc.engine, AuthStartOptions{Method: tc.method})
			if err != nil {
				t.Fatalf("Start failed: %v", err)
			}
			awaiting := waitForAuthStatus(t, mgr, tc.engine, started.SessionID, AuthStatusAwaitingUser)
			if awaiting.Mode != AuthModeCodePaste {
				t.Fatalf("Mode = %q, want %q", awaiting.Mode, AuthModeCodePaste)
			}
			if !strings.HasPrefix(awaiting.AuthURL, tc.wantURLPref) {
				t.Fatalf("AuthURL = %q, want prefix %q", awaiting.AuthURL, tc.wantURLPref)
			}

			if _, err := mgr.Submit(context.Background(), tc.engine, started.SessionID, "  "+tc.code+"  "); err != nil {
				t.Fatalf("Submit failed: %v", err)
			}
			succeeded := waitForAuthStatus(t, mgr, tc.engine, started.SessionID, AuthStatusSucceeded)
			if succeeded.EngineState == nil || succeeded.EngineState.Status != "ready" {
				t.Fatalf("expected ready EngineState, got %+v", succeeded.EngineState)
			}
		})
	}

	// Verify invalid Claude method is rejected.
	mgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Discover: helperDiscover(t, "claude_code"),
	})
	defer mgr.Close()
	if _, err := mgr.Start(context.Background(), config.RuntimeClaude, AuthStartOptions{Method: "invalid"}); !errors.Is(err, ErrAuthInputInvalid) {
		t.Fatalf("expected ErrAuthInputInvalid for invalid Claude method, got %v", err)
	}
}

func TestAuthLoopbackCallbackReplayAndNoRedirect(t *testing.T) {
	releaseFile := filepath.Join(t.TempDir(), "release")
	var redirectTargetHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer redirectTarget.Close()

	var receivedPath, receivedQuery string
	var cbMu sync.Mutex
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cbMu.Lock()
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		cbMu.Unlock()
		_ = os.WriteFile(releaseFile, []byte("cb-received"), 0o600)
		// Return a 302 redirect to verify AuthManager never follows redirects.
		http.Redirect(w, r, redirectTarget.URL+"/should-not-follow", http.StatusFound)
	}))
	defer callbackServer.Close()

	parsedServer, err := url.Parse(callbackServer.URL)
	if err != nil {
		t.Fatalf("parse callback server URL: %v", err)
	}
	port := parsedServer.Port()

	mgr := NewAuthManager(AuthManagerOptions{
		Enabled: func() bool { return true },
		Discover: helperDiscover(
			t,
			"callback_wait",
			"DENOVA_AUTH_REDIRECT_PORT="+port,
			"DENOVA_AUTH_RELEASE_FILE="+releaseFile,
		),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	defer mgr.Close()

	started, err := mgr.Start(context.Background(), config.RuntimeAntigravity, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusAwaitingUser)

	callbackURL := fmt.Sprintf("http://localhost:%s/callback?code=abc123&state=xyz789", port)
	if _, err := mgr.Submit(context.Background(), config.RuntimeAntigravity, started.SessionID, callbackURL); err != nil {
		t.Fatalf("Submit callback URL failed: %v", err)
	}

	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusSucceeded)

	cbMu.Lock()
	gotPath, gotQuery := receivedPath, receivedQuery
	cbMu.Unlock()
	if gotPath != "/callback" || gotQuery != "code=abc123&state=xyz789" {
		t.Fatalf("replayed callback = %q?%q, want /callback?code=abc123&state=xyz789", gotPath, gotQuery)
	}
	if redirectTargetHits.Load() != 0 {
		t.Fatalf("callback client followed redirect (%d hits)", redirectTargetHits.Load())
	}
}

func TestAuthSSRFProtectionBlocksUnauthorizedTargets(t *testing.T) {
	var otherPortHits atomic.Int32
	otherServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherPortHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer otherServer.Close()
	otherURL, _ := url.Parse(otherServer.URL)
	otherPort := otherURL.Port()

	// Pick a distinct listening port as the authorized redirect_uri port.
	allowedLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen allowed port: %v", err)
	}
	allowedPort := strconv.Itoa(allowedLn.Addr().(*net.TCPAddr).Port)
	_ = allowedLn.Close()

	mgr := NewAuthManager(AuthManagerOptions{
		Enabled: func() bool { return true },
		Discover: helperDiscover(
			t,
			"callback_wait",
			"DENOVA_AUTH_REDIRECT_PORT="+allowedPort,
		),
	})
	defer mgr.Close()

	started, err := mgr.Start(context.Background(), config.RuntimeAntigravity, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusAwaitingUser)

	maliciousInputs := []string{
		"http://169.254.169.254/latest",
		"https://evil.test/cb",
		fmt.Sprintf("http://127.0.0.1:%s/api/settings", otherPort),
		fmt.Sprintf("http://localhost:%s/api/settings", otherPort),
		fmt.Sprintf("https://localhost:%s/callback?code=abc", allowedPort),
		fmt.Sprintf("http://user@127.0.0.1:%s/callback?code=abc", allowedPort),
		fmt.Sprintf("http://localhost.evil.test:%s/callback?code=abc", allowedPort),
		fmt.Sprintf("http://[::1]:%s/callback?code=abc", allowedPort),
		"file:///etc/passwd",
		"//169.254.169.254/latest",
	}

	for _, bad := range maliciousInputs {
		if _, err := mgr.Submit(context.Background(), config.RuntimeAntigravity, started.SessionID, bad); !errors.Is(err, ErrAuthInputInvalid) {
			t.Errorf("Submit(%q) error = %v, want ErrAuthInputInvalid", bad, err)
		}
	}
	if otherPortHits.Load() != 0 {
		t.Fatalf("SSRF target on another loopback port was hit %d times", otherPortHits.Load())
	}
	// Session should remain awaiting_user after rejected inputs.
	snap, err := mgr.Get(config.RuntimeAntigravity, started.SessionID)
	if err != nil || snap.Status != AuthStatusAwaitingUser {
		t.Fatalf("expected session to remain awaiting_user, got (%+v, err=%v)", snap, err)
	}
}

func TestAuthControlCharacterInjectionAndInputLimitsRejected(t *testing.T) {
	mgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Discover: helperDiscover(t, "callback_wait", "DENOVA_AUTH_REDIRECT_PORT=43210"),
	})
	defer mgr.Close()

	started, err := mgr.Start(context.Background(), config.RuntimeAntigravity, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusAwaitingUser)

	invalidInputs := []string{
		"",
		"   \t  ",
		"abc\nrm -rf /",
		"abc\rdef",
		"abc\x00",
		"abc\n",
		"abc\x1b[31m",
		strings.Repeat("a", 4097),
	}
	for _, bad := range invalidInputs {
		if _, err := mgr.Submit(context.Background(), config.RuntimeAntigravity, started.SessionID, bad); !errors.Is(err, ErrAuthInputInvalid) {
			t.Errorf("Submit(%q) error = %v, want ErrAuthInputInvalid", bad, err)
		}
	}
}

func TestAuthLifecycleReplaceTTLCancelAndClose(t *testing.T) {
	t.Run("replace_active_session_cancels_previous", func(t *testing.T) {
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:  func() bool { return true },
			Discover: helperDiscover(t, "codex_block"),
		})
		defer mgr.Close()

		first, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("first Start failed: %v", err)
		}
		waitForAuthStatus(t, mgr, config.RuntimeCodex, first.SessionID, AuthStatusAwaitingUser)

		second, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("second Start failed: %v", err)
		}
		if second.SessionID == first.SessionID {
			t.Fatalf("expected distinct session IDs")
		}
		prev, err := mgr.Get(config.RuntimeCodex, first.SessionID)
		if err != nil || prev.Status != AuthStatusCancelled {
			t.Fatalf("expected first session cancelled, got (%+v, err=%v)", prev, err)
		}
		waitForAuthStatus(t, mgr, config.RuntimeCodex, second.SessionID, AuthStatusAwaitingUser)
	})

	t.Run("explicit_cancel_is_idempotent", func(t *testing.T) {
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:  func() bool { return true },
			Discover: helperDiscover(t, "codex_block"),
		})
		defer mgr.Close()

		started, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusAwaitingUser)

		firstCancel, err := mgr.Cancel(config.RuntimeCodex, started.SessionID)
		if err != nil || firstCancel.Status != AuthStatusCancelled {
			t.Fatalf("first Cancel = (%+v, %v), want cancelled", firstCancel, err)
		}
		secondCancel, err := mgr.Cancel(config.RuntimeCodex, started.SessionID)
		if err != nil || secondCancel.Status != AuthStatusCancelled {
			t.Fatalf("second Cancel = (%+v, %v), want idempotent cancelled", secondCancel, err)
		}
	})

	t.Run("ttl_expiration_terminates_process_and_expires_session", func(t *testing.T) {
		pidFile := filepath.Join(t.TempDir(), "child.pid")
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:  func() bool { return true },
			TTL:      350 * time.Millisecond,
			Discover: helperDiscover(t, "codex_block", "DENOVA_AUTH_PID_FILE="+pidFile),
		})
		defer mgr.Close()

		started, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusAwaitingUser)
		mgr.mu.Lock()
		session := mgr.byID[started.SessionID]
		mgr.mu.Unlock()

		// Wait past TTL so timeout fires and expires the session.
		time.Sleep(550 * time.Millisecond)

		if _, err := mgr.Get(config.RuntimeCodex, started.SessionID); !errors.Is(err, ErrAuthSessionNotFound) {
			t.Fatalf("expected ErrAuthSessionNotFound after TTL expiry, got %v", err)
		}
		if _, err := mgr.Submit(context.Background(), config.RuntimeCodex, started.SessionID, "code"); !errors.Is(err, ErrAuthSessionNotFound) {
			t.Fatalf("expected ErrAuthSessionNotFound on Submit after TTL expiry, got %v", err)
		}
		// Close must return promptly once TTL already terminated and reaped the child.
		done := make(chan struct{})
		go func() {
			mgr.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Close blocked waiting for expired session process")
		}
		if snap := session.snapshot(); snap.Status != AuthStatusFailed || snap.ErrorKey != "agentRuntime.auth.expired" {
			t.Fatalf("expired session snapshot = %+v, want status=failed error_key=agentRuntime.auth.expired", snap)
		}
	})
}

func TestAuthLogoutBusyProtectionAndCleanup(t *testing.T) {
	t.Run("busy_returns_err_operation_active", func(t *testing.T) {
		var closeIdleCalled, probeCalled atomic.Int32
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled: func() bool { return true },
			IsBusy:  func(config.RuntimeID) bool { return true },
			CloseIdle: func(config.RuntimeID) error {
				closeIdleCalled.Add(1)
				return nil
			},
			Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
				probeCalled.Add(1)
				return engineDescriptor(id), nil
			},
			Discover: helperDiscover(t, "codex_logout"),
		})
		defer mgr.Close()

		if _, err := mgr.Logout(context.Background(), config.RuntimeCodex); !errors.Is(err, ErrOperationActive) {
			t.Fatalf("Logout when busy = %v, want ErrOperationActive", err)
		}
		if closeIdleCalled.Load() != 0 || probeCalled.Load() != 0 {
			t.Fatalf("busy Logout must not call CloseIdle or Probe")
		}
	})

	t.Run("codex_and_claude_logout_invoke_cli_close_idle_and_probe", func(t *testing.T) {
		for _, tc := range []struct {
			engine config.RuntimeID
			mode   string
			want   string
		}{
			{config.RuntimeCodex, "codex_logout", "codex-logged-out"},
			{config.RuntimeClaude, "claude_logout", "claude-logged-out"},
		} {
			marker := filepath.Join(t.TempDir(), "logout.marker")
			var closedIdle, probed atomic.Int32
			mgr := NewAuthManager(AuthManagerOptions{
				Enabled:  func() bool { return true },
				Discover: helperDiscover(t, tc.mode, "DENOVA_AUTH_LOGOUT_FILE="+marker),
				CloseIdle: func(id config.RuntimeID) error {
					if id != tc.engine {
						t.Errorf("CloseIdle id = %q, want %q", id, tc.engine)
					}
					closedIdle.Add(1)
					return nil
				},
				Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
					probed.Add(1)
					desc := engineDescriptor(id)
					desc.Status = "auth_required"
					return desc, nil
				},
			})
			desc, err := mgr.Logout(context.Background(), tc.engine)
			mgr.Close()
			if err != nil {
				t.Fatalf("%s Logout failed: %v", tc.engine, err)
			}
			if desc.Status != "auth_required" || closedIdle.Load() != 1 || probed.Load() != 1 {
				t.Fatalf("%s unexpected logout result: desc=%+v closedIdle=%d probed=%d", tc.engine, desc, closedIdle.Load(), probed.Load())
			}
			body, err := os.ReadFile(marker)
			if err != nil || string(body) != tc.want {
				t.Fatalf("%s logout CLI marker = (%q, %v), want %q", tc.engine, string(body), err, tc.want)
			}
		}
	})

	t.Run("antigravity_logout_clears_credentials_and_preserves_user_data", func(t *testing.T) {
		fakeHome := t.TempDir()
		geminiDir := filepath.Join(fakeHome, ".gemini")
		cliDir := filepath.Join(geminiDir, "antigravity-cli")
		convDir := filepath.Join(geminiDir, "conversations")
		cliConvDir := filepath.Join(cliDir, "conversations")
		cfgDir := filepath.Join(geminiDir, "config")
		for _, dir := range []string{geminiDir, cliDir, convDir, cliConvDir, cfgDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir %s: %v", dir, err)
			}
		}

		// Credential files that MUST be removed.
		removePaths := []string{
			filepath.Join(geminiDir, "oauth_creds.json"),
			filepath.Join(geminiDir, "google_accounts.json"),
			filepath.Join(cliDir, "token.json"),
			filepath.Join(cliDir, "auth.json"),
		}
		for _, p := range removePaths {
			if err := os.WriteFile(p, []byte(`{"token":"secret"}`), 0o600); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}

		// User data files that MUST be preserved.
		preservePaths := []string{
			filepath.Join(geminiDir, "settings.json"),
			filepath.Join(cliDir, "settings.json"),
			filepath.Join(convDir, "story-1.jsonl"),
			filepath.Join(cliConvDir, "chat-1.jsonl"),
			filepath.Join(cfgDir, "hooks.json"),
		}
		for _, p := range preservePaths {
			if err := os.WriteFile(p, []byte(`{"keep":true}`), 0o600); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}

		var closedIdle, probed atomic.Int32
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:     func() bool { return true },
			UserHomeDir: func() (string, error) { return fakeHome, nil },
			CloseIdle: func(id config.RuntimeID) error {
				closedIdle.Add(1)
				return nil
			},
			Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
				probed.Add(1)
				desc := engineDescriptor(id)
				desc.Status = "auth_required"
				return desc, nil
			},
		})
		defer mgr.Close()

		desc, err := mgr.Logout(context.Background(), config.RuntimeAntigravity)
		if err != nil {
			t.Fatalf("Antigravity Logout failed: %v", err)
		}
		if desc.Status != "auth_required" || closedIdle.Load() != 1 || probed.Load() != 1 {
			t.Fatalf("unexpected logout outcome: desc=%+v closedIdle=%d probed=%d", desc, closedIdle.Load(), probed.Load())
		}

		for _, p := range removePaths {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("expected credential file %s to be deleted, stat err=%v", p, err)
			}
		}
		for _, p := range preservePaths {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("expected user data file %s to be preserved, stat err=%v", p, err)
			}
		}
	})
}

func TestAuthDisabledReturnsUnsupportedEverywhere(t *testing.T) {
	mgr := NewAuthManager(AuthManagerOptions{
		Enabled: func() bool { return false },
	})
	defer mgr.Close()

	for _, id := range []config.RuntimeID{config.RuntimeCodex, config.RuntimeClaude, config.RuntimeAntigravity, config.RuntimeNative, config.RuntimeGrok} {
		if mgr.Supported(id) {
			t.Errorf("Supported(%q) = true when disabled", id)
		}
		if _, err := mgr.Start(context.Background(), id, AuthStartOptions{}); !errors.Is(err, ErrAuthUnsupported) {
			t.Errorf("Start(%q) = %v, want ErrAuthUnsupported", id, err)
		}
		if _, err := mgr.Get(id, "any-session"); !errors.Is(err, ErrAuthUnsupported) {
			t.Errorf("Get(%q) = %v, want ErrAuthUnsupported", id, err)
		}
		if _, err := mgr.Submit(context.Background(), id, "any-session", "abc"); !errors.Is(err, ErrAuthUnsupported) {
			t.Errorf("Submit(%q) = %v, want ErrAuthUnsupported", id, err)
		}
		if _, err := mgr.Cancel(id, "any-session"); !errors.Is(err, ErrAuthUnsupported) {
			t.Errorf("Cancel(%q) = %v, want ErrAuthUnsupported", id, err)
		}
		if _, err := mgr.Logout(context.Background(), id); !errors.Is(err, ErrAuthUnsupported) {
			t.Errorf("Logout(%q) = %v, want ErrAuthUnsupported", id, err)
		}
	}
}

func TestAuthLogAndErrorRedaction(t *testing.T) {
	capture := &logCaptureHandler{}
	logger := slog.New(capture)

	// 1. Run Codex device_code flow.
	codexRelease := filepath.Join(t.TempDir(), "codex.rel")
	codexMgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Logger:   logger,
		Discover: helperDiscover(t, "codex_device", "DENOVA_AUTH_RELEASE_FILE="+codexRelease),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	codexSnap, err := codexMgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
	if err != nil {
		t.Fatalf("codex Start: %v", err)
	}
	waitForAuthStatus(t, codexMgr, config.RuntimeCodex, codexSnap.SessionID, AuthStatusAwaitingUser)
	_ = os.WriteFile(codexRelease, []byte("go"), 0o600)
	waitForAuthStatus(t, codexMgr, config.RuntimeCodex, codexSnap.SessionID, AuthStatusSucceeded)
	codexMgr.Close()

	// 2. Run Claude code_paste flow with secret code.
	secretCode := "SUPER_SECRET_AUTH_CODE_987654"
	claudeMgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Logger:   logger,
		Discover: helperDiscover(t, "claude_code", "DENOVA_AUTH_EXPECT_CODE="+secretCode),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	claudeSnap, err := claudeMgr.Start(context.Background(), config.RuntimeClaude, AuthStartOptions{})
	if err != nil {
		t.Fatalf("claude Start: %v", err)
	}
	waitForAuthStatus(t, claudeMgr, config.RuntimeClaude, claudeSnap.SessionID, AuthStatusAwaitingUser)
	if _, err := claudeMgr.Submit(context.Background(), config.RuntimeClaude, claudeSnap.SessionID, secretCode); err != nil {
		t.Fatalf("claude Submit: %v", err)
	}
	waitForAuthStatus(t, claudeMgr, config.RuntimeClaude, claudeSnap.SessionID, AuthStatusSucceeded)
	claudeMgr.Close()

	// 3. Trigger a failed loopback callback replay (closed port) to verify callback URL/query never leaks into error or logs.
	deadLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen dead port: %v", err)
	}
	deadPort := strconv.Itoa(deadLn.Addr().(*net.TCPAddr).Port)
	_ = deadLn.Close()

	cbMgr := NewAuthManager(AuthManagerOptions{
		Enabled:  func() bool { return true },
		Logger:   logger,
		Discover: helperDiscover(t, "callback_wait", "DENOVA_AUTH_REDIRECT_PORT="+deadPort),
	})
	cbSnap, err := cbMgr.Start(context.Background(), config.RuntimeAntigravity, AuthStartOptions{})
	if err != nil {
		t.Fatalf("cb Start: %v", err)
	}
	waitForAuthStatus(t, cbMgr, config.RuntimeAntigravity, cbSnap.SessionID, AuthStatusAwaitingUser)
	secretCallbackToken := "CALLBACK_SECRET_TOKEN_XYZ789"
	_, cbErr := cbMgr.Submit(context.Background(), config.RuntimeAntigravity, cbSnap.SessionID, fmt.Sprintf("http://localhost:%s/callback?code=%s", deadPort, secretCallbackToken))
	if cbErr == nil {
		t.Fatal("expected error when loopback listener is closed")
	}
	if strings.Contains(cbErr.Error(), secretCallbackToken) || strings.Contains(cbErr.Error(), "localhost") {
		t.Fatalf("callback replay error leaked sensitive URL data: %q", cbErr.Error())
	}
	cbMgr.Close()

	logs := capture.dump()
	forbidden := []string{
		"https://auth.openai.com/codex/device",
		"ABCD-1234",
		"https://claude.ai/oauth/authorize",
		"https://accounts.google.com/o/oauth2/v2/auth",
		secretCode,
		secretCallbackToken,
		"RAW_PTY_SECRET_SENTINEL_CODEX",
		"RAW_PTY_SECRET_SENTINEL_CLAUDE",
		"RAW_PTY_SECRET_SENTINEL_CB",
	}
	for _, secret := range forbidden {
		if strings.Contains(logs, secret) {
			t.Errorf("log output leaked sensitive value %q:\n%s", secret, logs)
		}
	}
}

func TestAuthPromptParserIgnoresDocsURLsAndStripsOSC8(t *testing.T) {
	claudeOut := stripANSI(
		"Docs: https://docs.anthropic.com/en/docs/claude-code\r\n" +
			"\x1b]8;;https://claude.ai/oauth/authorize?client_id=1&redirect_uri=http%3A%2F%2Flocalhost%3A49152%2Fcb\x07" +
			"https://claude.ai/oauth/authorize?client_id=1&redirect_uri=http%3A%2F%2Flocalhost%3A49152%2Fcb\x1b]8;;\x07\r\n",
	)
	gotClaude := parseAuthPrompt(config.RuntimeClaude, claudeOut, false)
	if !gotClaude.Ready || gotClaude.RedirectPort != 49152 || !strings.HasPrefix(gotClaude.AuthURL, "https://claude.ai/oauth/authorize?") {
		t.Fatalf("unexpected Claude parse result: %+v", gotClaude)
	}

	agyOut := stripANSI("Help: https://support.google.com/gemini\nSign in: https://accounts.google.com/o/oauth2/v2/auth?redirect_uri=http%3A%2F%2F127.0.0.1%3A40123%2Fcb\n")
	gotAgy := parseAuthPrompt(config.RuntimeAntigravity, agyOut, false)
	if !gotAgy.Ready || gotAgy.RedirectPort != 40123 || !strings.HasPrefix(gotAgy.AuthURL, "https://accounts.google.com/o/oauth2/v2/auth?") {
		t.Fatalf("unexpected Antigravity parse result: %+v", gotAgy)
	}

	codexOut := stripANSI("Docs: https://platform.openai.com/docs\nOpen https://auth.openai.com/codex/device\nCode: \x1b[1mG7K2-9MP4X\x1b[0m\n")
	gotCodex := parseAuthPrompt(config.RuntimeCodex, codexOut, false)
	if !gotCodex.Ready || gotCodex.AuthURL != "https://auth.openai.com/codex/device" || gotCodex.UserCode != "G7K2-9MP4X" {
		t.Fatalf("unexpected Codex parse result: %+v", gotCodex)
	}
}

func TestAuthPromptParserPrefersClaudeComOAuthAndOSC8Target(t *testing.T) {
	// Claude Code 2.1.332+ emits https://claude.com/cai/oauth/authorize?... and
	// may print a generic https://claude.ai link earlier in the banner.
	claudeBanner := "Visit https://claude.ai for account info.\n" +
		"Open https://claude.com/cai/oauth/authorize?client_id=cid&redirect_uri=http%3A%2F%2Flocalhost%3A48123%2Fcallback&state=st1\n"
	gotClaude := parseAuthPromptRaw(config.RuntimeClaude, claudeBanner, false)
	wantClaudeURL := "https://claude.com/cai/oauth/authorize?client_id=cid&redirect_uri=http%3A%2F%2Flocalhost%3A48123%2Fcallback&state=st1"
	if !gotClaude.Ready || gotClaude.AuthURL != wantClaudeURL || gotClaude.RedirectPort != 48123 {
		t.Fatalf("claude.com parse = %+v, want ready url=%q port=48123", gotClaude, wantClaudeURL)
	}

	// Codex prints a ChatGPT settings link before the actual device verification URL.
	codexBanner := "Enable device code auth at https://chatgpt.com/#settings/Security\n" +
		"Then open https://auth.openai.com/codex/device and enter QWER-5678\n"
	gotCodex := parseAuthPromptRaw(config.RuntimeCodex, codexBanner, false)
	if !gotCodex.Ready || gotCodex.AuthURL != "https://auth.openai.com/codex/device" || gotCodex.UserCode != "QWER-5678" {
		t.Fatalf("codex priority parse = %+v, want auth.openai.com/codex/device and QWER-5678", gotCodex)
	}

	// Soft-wrapped OSC 8 hyperlink: visible text is split across lines, but the
	// OSC 8 target sequence retains the complete OAuth URL and redirect_uri.
	intactOSC8URL := "https://claude.com/cai/oauth/authorize?client_id=osc8&redirect_uri=http%3A%2F%2Flocalhost%3A49222%2Fcb&code_challenge=xyz"
	wrappedOSC8 := "\x1b]8;;" + intactOSC8URL + "\x07" +
		"https://claude.com/cai/oauth/auth\r\norize?client_id=osc8\x1b]8;;\x07\r\n"
	gotOSC8 := parseAuthPromptRaw(config.RuntimeClaude, wrappedOSC8, false)
	if !gotOSC8.Ready || gotOSC8.AuthURL != intactOSC8URL || gotOSC8.RedirectPort != 49222 {
		t.Fatalf("wrapped OSC 8 parse = %+v, want intact url=%q port=49222", gotOSC8, intactOSC8URL)
	}

	// A low-priority banner link alone must not mark Ready before partial-flush.
	bannerOnly := parseAuthPromptRaw(config.RuntimeClaude, "Welcome: https://claude.ai\n", false)
	if bannerOnly.Ready || bannerOnly.URLPriority != 1 {
		t.Fatalf("banner-only parse = %+v, want ready=false priority=1", bannerOnly)
	}
}

func TestAuthStreamingBannerUpgradesToOAuthURL(t *testing.T) {
	expectedCode := "BANNER_UPGRADE_CODE_77"
	mgr := NewAuthManager(AuthManagerOptions{
		Enabled:     func() bool { return true },
		PreferPipes: true,
		Discover:    helperDiscover(t, "claude_banner_then_oauth", "DENOVA_AUTH_EXPECT_CODE="+expectedCode),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	defer mgr.Close()

	started, err := mgr.Start(context.Background(), config.RuntimeClaude, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	wantURL := "https://claude.com/cai/oauth/authorize?client_id=banner&redirect_uri=http%3A%2F%2Flocalhost%3A48777%2Fcallback&state=s2"
	deadline := time.Now().Add(5 * time.Second)
	var snap AuthSessionSnapshot
	for time.Now().Before(deadline) {
		snap, err = mgr.Get(config.RuntimeClaude, started.SessionID)
		if err == nil && snap.Status == AuthStatusAwaitingUser && snap.AuthURL == wantURL {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if snap.Status != AuthStatusAwaitingUser || snap.AuthURL != wantURL {
		t.Fatalf("session did not upgrade from banner URL to OAuth URL: %+v (err=%v)", snap, err)
	}
	if _, err := mgr.Submit(context.Background(), config.RuntimeClaude, started.SessionID, expectedCode); err != nil {
		t.Fatalf("Submit after upgrade failed: %v", err)
	}
	waitForAuthStatus(t, mgr, config.RuntimeClaude, started.SessionID, AuthStatusSucceeded)
}

func TestAuthSubmitDoesNotRollbackAfterProcessExit(t *testing.T) {
	releaseFile := filepath.Join(t.TempDir(), "release-fast")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback: %v", err)
	}
	defer ln.Close()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseProbe) }) }
	defer release()

	// Simulate a CLI callback listener that receives the request, lets the CLI
	// exit 0 immediately, and closes the socket without writing an HTTP response
	// once watchSessionProcess has observed exit 0 and entered Probe.
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		_ = os.WriteFile(releaseFile, []byte("done"), 0o600)
		select {
		case <-probeStarted:
		case <-time.After(3 * time.Second):
		}
		_ = conn.Close()
	}()

	mgr := NewAuthManager(AuthManagerOptions{
		Enabled:     func() bool { return true },
		PreferPipes: true,
		Discover: helperDiscover(
			t,
			"callback_wait",
			"DENOVA_AUTH_REDIRECT_PORT="+port,
			"DENOVA_AUTH_RELEASE_FILE="+releaseFile,
		),
		Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
			close(probeStarted)
			<-releaseProbe
			desc := engineDescriptor(id)
			desc.Status = "ready"
			return desc, nil
		},
	})
	defer mgr.Close()

	started, err := mgr.Start(context.Background(), config.RuntimeAntigravity, AuthStartOptions{})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusAwaitingUser)

	callbackURL := fmt.Sprintf("http://localhost:%s/callback?code=fast-exit&state=ok", port)
	submitted, err := mgr.Submit(context.Background(), config.RuntimeAntigravity, started.SessionID, callbackURL)
	if err != nil {
		t.Fatalf("Submit should not fail when process already exited during callback: %v", err)
	}
	if submitted.Status != AuthStatusCompleting {
		t.Fatalf("expected completing status while probe is in flight, got %+v", submitted)
	}
	release()
	waitForAuthStatus(t, mgr, config.RuntimeAntigravity, started.SessionID, AuthStatusSucceeded)
}

func TestAuthFailureOnNonZeroExitAndUnreadyProbe(t *testing.T) {
	t.Run("nonzero_exit_marks_failed", func(t *testing.T) {
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:  func() bool { return true },
			Discover: helperDiscover(t, "exit_fail"),
		})
		defer mgr.Close()

		started, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		failed := waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusFailed)
		if failed.ErrorKey != "agentRuntime.auth.failed" {
			t.Fatalf("ErrorKey = %q, want agentRuntime.auth.failed", failed.ErrorKey)
		}
	})

	t.Run("probe_not_ready_marks_failed", func(t *testing.T) {
		releaseFile := filepath.Join(t.TempDir(), "release")
		_ = os.WriteFile(releaseFile, []byte("go"), 0o600)
		mgr := NewAuthManager(AuthManagerOptions{
			Enabled:  func() bool { return true },
			Discover: helperDiscover(t, "codex_device", "DENOVA_AUTH_RELEASE_FILE="+releaseFile),
			Probe: func(_ context.Context, id config.RuntimeID) (EngineDescriptor, error) {
				desc := engineDescriptor(id)
				desc.Status = "auth_required"
				return desc, nil
			},
		})
		defer mgr.Close()

		started, err := mgr.Start(context.Background(), config.RuntimeCodex, AuthStartOptions{})
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		failed := waitForAuthStatus(t, mgr, config.RuntimeCodex, started.SessionID, AuthStatusFailed)
		if failed.EngineState == nil || failed.EngineState.Status != "auth_required" {
			t.Fatalf("expected auth_required EngineState, got %+v", failed.EngineState)
		}
	})
}
