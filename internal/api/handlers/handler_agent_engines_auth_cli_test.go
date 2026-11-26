package handlers_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"denova/config"
	agentruntime "denova/internal/agents/runtime"
	"denova/internal/agents/runtime/external"
	"denova/internal/api"
	runtimeapp "denova/internal/app"
)

const apiAuthHelperEnv = "DENOVA_API_AUTH_HELPER_MODE"

// TestAPIAuthHelperProcess acts as a fake Antigravity/Codex CLI for HTTP OAuth
// handler integration tests when DENOVA_API_AUTH_HELPER_MODE is set.
func TestAPIAuthHelperProcess(t *testing.T) {
	mode := os.Getenv(apiAuthHelperEnv)
	if mode == "" {
		return
	}
	switch mode {
	case "antigravity_code":
		fmt.Print("https://accounts.google.com/o/oauth2/v2/auth?client_id=test&redirect_uri=http%3A%2F%2Flocalhost%3A44111%2Fcallback&state=s1\nPaste code: ")
		reader := bufio.NewReader(os.Stdin)
		var out strings.Builder
		for {
			b, err := reader.ReadByte()
			if err != nil || b == '\n' || b == '\r' {
				break
			}
			out.WriteByte(b)
		}
		if out.String() != os.Getenv("DENOVA_API_AUTH_EXPECT_CODE") {
			os.Exit(4)
		}
		os.Exit(0)
	case "antigravity_block":
		fmt.Print("https://accounts.google.com/o/oauth2/v2/auth?client_id=test&redirect_uri=http%3A%2F%2Flocalhost%3A44112%2Fcallback&state=s2\n")
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		os.Exit(99)
	}
}

type fakeVersionedConn struct {
	mu      sync.Mutex
	version string
	state   external.ConnectionState
}

func (c *fakeVersionedConn) Version() string { return c.version }
func (c *fakeVersionedConn) Run(context.Context, external.Input, external.Host) (external.Result, error) {
	return external.Result{}, nil
}
func (c *fakeVersionedConn) Status() external.ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}
func (c *fakeVersionedConn) Check(context.Context) (external.ConnectionState, error) {
	return c.Status(), nil
}
func (c *fakeVersionedConn) Models(context.Context) (external.Models, error) {
	return external.Models{
		Items:     []external.Model{{ID: "test-model", DisplayName: "Test Model", Efforts: []string{"medium"}}},
		DefaultID: "test-model",
	}, nil
}
func (c *fakeVersionedConn) Close() error {
	c.mu.Lock()
	c.state.Status = "unavailable"
	c.mu.Unlock()
	return nil
}

type fakeCLISource struct {
	release agentruntime.CLIRelease
	err     error
}

func (s fakeCLISource) Resolve(context.Context, *http.Client, string) (agentruntime.CLIRelease, error) {
	if s.err != nil {
		return agentruntime.CLIRelease{}, s.err
	}
	return s.release, nil
}

type lanTestServer struct {
	baseURL string
	cookie  string
	client  *http.Client
	app     *runtimeapp.App
}

func startLANTestServer(t *testing.T, engines *agentruntime.Engines) *lanTestServer {
	t.Helper()
	root := t.TempDir()
	hash, err := config.HashRemoteAccessPassword("secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	cfg := &config.Config{
		OpenAIModel:              "test-model",
		NovaDir:                  root,
		Workspace:                root,
		ResumeLastWorkspace:      false,
		OpenAIBaseURL:            "http://127.0.0.1:1/v1",
		AllowLANAccess:           true,
		RemoteAccessUsername:     "reader",
		RemoteAccessPasswordHash: hash,
	}
	application, err := runtimeapp.NewWithOptions(context.Background(), cfg, runtimeapp.WithAgentEngines(engines))
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	t.Cleanup(application.Close)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	server := api.NewServerWithListener(application, port, listener)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.RunUntil(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("HTTP server did not stop cleanly")
		}
	})

	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	baseURL := "http://" + listener.Addr().String()

	// Authenticate as a non-loopback LAN client (simulated via X-Forwarded-For).
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/auth/login", strings.NewReader(`{"username":"reader","password":"secret"}`))
	if err != nil {
		t.Fatalf("build login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "192.168.1.42")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("LAN login failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("LAN login status = %d body = %s", resp.StatusCode, string(body))
	}
	cookie := strings.Split(resp.Header.Get("Set-Cookie"), ";")[0]
	if cookie == "" {
		t.Fatal("LAN login returned no session cookie")
	}
	return &lanTestServer{
		baseURL: baseURL,
		cookie:  cookie,
		client:  client,
		app:     application,
	}
}

func (s *lanTestServer) doLAN(t *testing.T, method, path, body string, authenticated bool, extraHeaders ...[2]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	req.Header.Set("X-Forwarded-For", "192.168.1.42")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		req.Header.Set("Cookie", s.cookie)
	}
	for _, h := range extraHeaders {
		req.Header.Set(h[0], h[1])
	}
	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s failed: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response %s %s: %v", method, path, err)
	}
	return resp.StatusCode, data
}

func decodeErrBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON %q: %v", string(raw), err)
	}
	return out
}

// TestAgentEngineCLIAndAuthUnsupportedWhenDisabled verifies Review Focus #5:
// in non-managed / non-container environments, GET /api/agent-runtimes omits
// managed and auth_supported, and all /cli/* and /auth/* routes return 400
// (never 403 from localHostEffectMiddleware).
func TestAgentEngineCLIAndAuthUnsupportedWhenDisabled(t *testing.T) {
	engines := agentruntime.NewEnginesWithOptions(agentruntime.EnginesOptions{
		CLIManager:  &agentruntime.CLIManagerOptions{Dir: "", GOOS: "windows", GOARCH: "amd64"},
		AuthManager: &agentruntime.AuthManagerOptions{Enabled: func() bool { return false }},
	})
	srv := startLANTestServer(t, engines)

	// Unauthenticated LAN requests must still be blocked by remoteAccessGate with 401.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/agent-runtimes/codex/cli/status", ""},
		{http.MethodPost, "/api/agent-runtimes/codex/cli/check-update", "{}"},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/start", "{}"},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/logout", "{}"},
	} {
		code, raw := srv.doLAN(t, tc.method, tc.path, tc.body, false)
		if code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s = %d body=%s, want 401", tc.method, tc.path, code, string(raw))
		}
	}

	// Authenticated LAN GET /api/agent-runtimes must omit managed and auth_supported.
	code, raw := srv.doLAN(t, http.MethodGet, "/api/agent-runtimes", "", true)
	if code != http.StatusOK {
		t.Fatalf("GET /api/agent-runtimes = %d %s", code, string(raw))
	}
	var catalogResp struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &catalogResp); err != nil || len(catalogResp.Items) == 0 {
		t.Fatalf("decode catalog: %v body=%s", err, string(raw))
	}
	for _, item := range catalogResp.Items {
		if _, ok := item["managed"]; ok {
			t.Fatalf("non-managed catalog item leaked managed: %v", item)
		}
		if _, ok := item["auth_supported"]; ok {
			t.Fatalf("non-managed catalog item leaked auth_supported: %v", item)
		}
	}

	// All 8 endpoints must return 400 capabilityUnsupported (never 403).
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/agent-runtimes/codex/cli/check-update", "{}"},
		{http.MethodPost, "/api/agent-runtimes/codex/cli/install", "{}"},
		{http.MethodGet, "/api/agent-runtimes/codex/cli/status", ""},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/start", "{}"},
		{http.MethodGet, "/api/agent-runtimes/codex/auth/sess-1", ""},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/sess-1/submit", `{"input":"abc"}`},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/sess-1/cancel", "{}"},
		{http.MethodPost, "/api/agent-runtimes/codex/auth/logout", "{}"},
	} {
		status, respRaw := srv.doLAN(t, tc.method, tc.path, tc.body, true)
		obj := decodeErrBody(t, respRaw)
		if status != http.StatusBadRequest || obj["code"] != "agentRuntime.capabilityUnsupported" {
			t.Fatalf("disabled %s %s = (%d, %v), want 400 agentRuntime.capabilityUnsupported", tc.method, tc.path, status, obj)
		}
	}

	// Unknown engine ID must return 404 agentRuntime.notFound.
	status, respRaw := srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/nonexistent/cli/status", "", true)
	if obj := decodeErrBody(t, respRaw); status != http.StatusNotFound || obj["code"] != "agentRuntime.notFound" {
		t.Fatalf("unknown engine cli status = (%d, %v), want 404 agentRuntime.notFound", status, obj)
	}
	status, respRaw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/nonexistent/auth/start", "{}", true)
	if obj := decodeErrBody(t, respRaw); status != http.StatusNotFound || obj["code"] != "agentRuntime.notFound" {
		t.Fatalf("unknown engine auth start = (%d, %v), want 404 agentRuntime.notFound", status, obj)
	}
}

func TestAgentEngineCLIAndAuthEndpointsOverAuthenticatedLAN(t *testing.T) {
	cliDir := t.TempDir()
	fakeHome := t.TempDir()
	geminiDir := filepath.Join(fakeHome, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		t.Fatalf("mkdir .gemini: %v", err)
	}
	if err := os.WriteFile(filepath.Join(geminiDir, "oauth_creds.json"), []byte(`{"token":"t"}`), 0o600); err != nil {
		t.Fatalf("write fake cred: %v", err)
	}

	binaryBytes := []byte("#!/bin/sh\necho 1.5.18\n")
	digest := sha256.Sum256(binaryBytes)
	downloadStarted := make(chan struct{}, 1)
	releaseDownload := make(chan struct{})
	dlServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case downloadStarted <- struct{}{}:
		default:
		}
		<-releaseDownload
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(binaryBytes)
	}))
	defer dlServer.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test exe: %v", err)
	}

	var (
		modeMu      sync.Mutex
		agyAuthMode = "antigravity_code"
		agyState    = external.ConnectionState{Status: "ready"}
	)
	expectedCode := "VALID_API_AUTH_CODE_42"

	engines := agentruntime.NewEnginesWithOptions(agentruntime.EnginesOptions{
		CLIManager: &agentruntime.CLIManagerOptions{
			Dir:        cliDir,
			GOOS:       "linux",
			GOARCH:     "amd64",
			HTTPClient: dlServer.Client(),
			Sources: map[config.RuntimeID]agentruntime.CLISource{
				config.RuntimeAntigravity: fakeCLISource{
					release: agentruntime.CLIRelease{
						Version:      "1.5.18",
						DownloadURL:  dlServer.URL + "/agy",
						HashAlgo:     "sha256",
						Digest:       digest[:],
						ExpectedSize: int64(len(binaryBytes)),
					},
				},
			},
			VerifyBinary: func(context.Context, config.RuntimeID, string) (string, error) {
				return "1.5.18", nil
			},
			// Exercise App's auto-update worker startup and shutdown without triggering an unwanted download mid-test.
			AutoUpdateDelay: time.Hour,
		},
		AuthManager: &agentruntime.AuthManagerOptions{
			Enabled:     func() bool { return true },
			UserHomeDir: func() (string, error) { return fakeHome, nil },
			PreferPipes: true,
			Discover: func(id config.RuntimeID) (string, []string, []string, error) {
				if id == config.RuntimeCodex {
					return "", nil, nil, agentruntime.ErrEngineNotInstalled
				}
				modeMu.Lock()
				mode := agyAuthMode
				modeMu.Unlock()
				return exe, []string{"-test.run=^TestAPIAuthHelperProcess$", "--"}, []string{
					apiAuthHelperEnv + "=" + mode,
					"DENOVA_API_AUTH_EXPECT_CODE=" + expectedCode,
				}, nil
			},
		},
		Factories: map[config.RuntimeID]func(context.Context) (external.Connection, error){
			config.RuntimeAntigravity: func(context.Context) (external.Connection, error) {
				modeMu.Lock()
				st := agyState
				modeMu.Unlock()
				return &fakeVersionedConn{version: "1.5.18", state: st}, nil
			},
		},
	})
	srv := startLANTestServer(t, engines)

	// 1. Catalog exposes managed + auth_supported on managed runtimes over LAN.
	code, raw := srv.doLAN(t, http.MethodGet, "/api/agent-runtimes", "", true)
	if code != http.StatusOK {
		t.Fatalf("GET /api/agent-runtimes = %d %s", code, string(raw))
	}
	var cat struct {
		Items []agentruntime.EngineDescriptor `json:"items"`
	}
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	var agyDesc *agentruntime.EngineDescriptor
	for i := range cat.Items {
		if cat.Items[i].ID == config.RuntimeAntigravity {
			agyDesc = &cat.Items[i]
		}
	}
	if agyDesc == nil || !agyDesc.AuthSupported || agyDesc.Managed == nil || !agyDesc.Managed.Managed {
		t.Fatalf("unexpected antigravity descriptor: %+v", agyDesc)
	}

	// 2. Strict JSON decoding rejects unknown fields on all mutating endpoints.
	for _, tc := range []struct{ path, body string }{
		{"/api/agent-runtimes/antigravity/cli/check-update", `{"extra":true}`},
		{"/api/agent-runtimes/antigravity/cli/install", `{"extra":true}`},
		{"/api/agent-runtimes/antigravity/auth/start", `{"extra":true}`},
		{"/api/agent-runtimes/antigravity/auth/s1/submit", `{"input":"a","extra":true}`},
		{"/api/agent-runtimes/antigravity/auth/s1/cancel", `{"extra":true}`},
		{"/api/agent-runtimes/antigravity/auth/logout", `{"extra":true}`},
	} {
		status, respRaw := srv.doLAN(t, http.MethodPost, tc.path, tc.body, true)
		if status != http.StatusBadRequest {
			t.Fatalf("strict JSON %s = %d %s, want 400", tc.path, status, string(respRaw))
		}
	}

	// 3. POST /cli/check-update -> 200 ManagedCLIStatus.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/cli/check-update", "{}", true)
	if code != http.StatusOK {
		t.Fatalf("check-update = %d %s", code, string(raw))
	}
	var cliStatus agentruntime.ManagedCLIStatus
	if err := json.Unmarshal(raw, &cliStatus); err != nil || cliStatus.LatestVersion != "1.5.18" {
		t.Fatalf("check-update body = %s err=%v", string(raw), err)
	}

	// 4. POST /cli/install -> 202 Accepted; concurrent install -> 409 updateBusy.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/cli/install", "{}", true)
	if code != http.StatusAccepted {
		t.Fatalf("install = %d %s, want 202", code, string(raw))
	}
	select {
	case <-downloadStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("download did not start")
	}

	busyCode, busyRaw := srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/cli/install", "{}", true, [2]string{"X-Denova-Locale", "en-US"})
	busyObj := decodeErrBody(t, busyRaw)
	if busyCode != http.StatusConflict || busyObj["code"] != "agentRuntime.updateBusy" {
		t.Fatalf("concurrent install = (%d, %v), want 409 agentRuntime.updateBusy", busyCode, busyObj)
	}
	close(releaseDownload)

	// 5. GET /cli/status -> 200 {"managed": ..., "engine": ...} converges to idle + installed.
	var statusResp struct {
		Managed agentruntime.ManagedCLIStatus `json:"managed"`
		Engine  agentruntime.EngineDescriptor `json:"engine"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, raw = srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/antigravity/cli/status", "", true)
		if code != http.StatusOK {
			t.Fatalf("cli/status = %d %s", code, string(raw))
		}
		if err := json.Unmarshal(raw, &statusResp); err != nil {
			t.Fatalf("decode cli/status: %v", err)
		}
		if statusResp.Managed.State == "idle" && statusResp.Managed.Installed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !statusResp.Managed.Installed || statusResp.Managed.InstalledVersion != "1.5.18" || statusResp.Engine.ID != config.RuntimeAntigravity {
		t.Fatalf("cli/status did not converge: %+v", statusResp)
	}

	// Unmanaged engine /cli/status -> 400 capabilityUnsupported.
	grokCode, grokRaw := srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/grok/cli/status", "", true)
	if obj := decodeErrBody(t, grokRaw); grokCode != http.StatusBadRequest || obj["code"] != "agentRuntime.capabilityUnsupported" {
		t.Fatalf("grok cli/status = (%d, %v), want 400 capabilityUnsupported", grokCode, obj)
	}

	// 6. Auth start when CLI not installed -> 409 agentRuntime.auth.notInstalled.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/codex/auth/start", "{}", true)
	if obj := decodeErrBody(t, raw); code != http.StatusConflict || obj["code"] != "agentRuntime.auth.notInstalled" {
		t.Fatalf("not installed auth start = (%d, %v), want 409 agentRuntime.auth.notInstalled", code, obj)
	}

	// 7. Unknown auth session -> 404 agentRuntime.auth.expired.
	code, raw = srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/antigravity/auth/does-not-exist", "", true)
	if obj := decodeErrBody(t, raw); code != http.StatusNotFound || obj["code"] != "agentRuntime.auth.expired" {
		t.Fatalf("missing auth session = (%d, %v), want 404 agentRuntime.auth.expired", code, obj)
	}

	// 8. Full code_paste auth start -> poll -> invalid SSRF submit (no secret leak) -> valid submit -> succeeded.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/start", "{}", true)
	if code != http.StatusOK {
		t.Fatalf("auth start = %d %s", code, string(raw))
	}
	var authSnap agentruntime.AuthSessionSnapshot
	if err := json.Unmarshal(raw, &authSnap); err != nil || authSnap.SessionID == "" || authSnap.Mode != agentruntime.AuthModeCodePaste {
		t.Fatalf("auth start snapshot = %+v err=%v", authSnap, err)
	}

	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, raw = srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/antigravity/auth/"+authSnap.SessionID, "", true)
		if code != http.StatusOK {
			t.Fatalf("auth poll = %d %s", code, string(raw))
		}
		if err := json.Unmarshal(raw, &authSnap); err != nil {
			t.Fatalf("decode auth poll: %v", err)
		}
		if authSnap.Status == agentruntime.AuthStatusAwaitingUser {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if authSnap.Status != agentruntime.AuthStatusAwaitingUser || authSnap.AuthURL == "" {
		t.Fatalf("auth session did not reach awaiting_user: %+v", authSnap)
	}

	// SSRF input must return 400 agentRuntime.auth.invalidInput without echoing secret detail.
	ssrfSecret := "TOP_SECRET_OAUTH_TOKEN_XYZ"
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/"+authSnap.SessionID+"/submit",
		fmt.Sprintf(`{"input":"http://169.254.169.254/cb?code=%s"}`, ssrfSecret), true)
	obj := decodeErrBody(t, raw)
	if code != http.StatusBadRequest || obj["code"] != "agentRuntime.auth.invalidInput" {
		t.Fatalf("invalid submit = (%d, %v), want 400 agentRuntime.auth.invalidInput", code, obj)
	}
	if bytes.Contains(raw, []byte(ssrfSecret)) || bytes.Contains(raw, []byte("169.254.169.254")) {
		t.Fatalf("auth error response leaked sensitive input: %s", string(raw))
	}
	if details, ok := obj["details"].(map[string]any); ok {
		if _, hasRawDetail := details["detail"]; hasRawDetail {
			t.Fatalf("auth error response must not expose raw detail: %s", string(raw))
		}
	}

	// Valid code submit -> 200 and converges to succeeded.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/"+authSnap.SessionID+"/submit",
		fmt.Sprintf(`{"input":"%s"}`, expectedCode), true)
	if code != http.StatusOK {
		t.Fatalf("valid submit = %d %s", code, string(raw))
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, raw = srv.doLAN(t, http.MethodGet, "/api/agent-runtimes/antigravity/auth/"+authSnap.SessionID, "", true)
		if code != http.StatusOK {
			t.Fatalf("auth completion poll = %d %s", code, string(raw))
		}
		_ = json.Unmarshal(raw, &authSnap)
		if authSnap.Status == agentruntime.AuthStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if authSnap.Status != agentruntime.AuthStatusSucceeded {
		t.Fatalf("auth session did not succeed: %+v", authSnap)
	}

	// Submitting again after completion -> 409 agentRuntime.auth.failed.
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/"+authSnap.SessionID+"/submit", `{"input":"late"}`, true)
	if obj := decodeErrBody(t, raw); code != http.StatusConflict || obj["code"] != "agentRuntime.auth.failed" {
		t.Fatalf("late submit = (%d, %v), want 409 agentRuntime.auth.failed", code, obj)
	}

	// 9. Start blocking session and cancel it -> 200 status=cancelled.
	modeMu.Lock()
	agyAuthMode = "antigravity_block"
	modeMu.Unlock()
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/start", "", true)
	if code != http.StatusOK {
		t.Fatalf("auth start with empty body = %d %s", code, string(raw))
	}
	var cancelSnap agentruntime.AuthSessionSnapshot
	if err := json.Unmarshal(raw, &cancelSnap); err != nil || cancelSnap.SessionID == "" {
		t.Fatalf("decode cancel target: %v", err)
	}
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/"+cancelSnap.SessionID+"/cancel", "{}", true)
	if code != http.StatusOK {
		t.Fatalf("auth cancel = %d %s", code, string(raw))
	}
	if err := json.Unmarshal(raw, &cancelSnap); err != nil || cancelSnap.Status != agentruntime.AuthStatusCancelled {
		t.Fatalf("cancel snapshot = %+v err=%v", cancelSnap, err)
	}

	// 10. Logout while active lease is held -> 409 agentRuntime.auth.busy; once released -> 200 EngineDescriptor.
	_, releaseLease, err := engines.Acquire(context.Background(), config.RuntimeSelection{
		Kind:        config.RuntimeAntigravity,
		Antigravity: &config.AntigravityRuntimeSettings{Model: "test-model"},
	}, config.Config{})
	if err != nil {
		t.Fatalf("Acquire antigravity: %v", err)
	}
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/logout", "{}", true)
	if obj := decodeErrBody(t, raw); code != http.StatusConflict || obj["code"] != "agentRuntime.auth.busy" {
		t.Fatalf("busy logout = (%d, %v), want 409 agentRuntime.auth.busy", code, obj)
	}
	releaseLease()

	modeMu.Lock()
	agyState = external.ConnectionState{Status: "auth_required"}
	modeMu.Unlock()
	code, raw = srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/antigravity/auth/logout", "{}", true)
	if code != http.StatusOK {
		t.Fatalf("idle logout = %d %s", code, string(raw))
	}
	var loggedOut agentruntime.EngineDescriptor
	if err := json.Unmarshal(raw, &loggedOut); err != nil || loggedOut.Status != "auth_required" {
		t.Fatalf("logout descriptor = %+v err=%v", loggedOut, err)
	}
	if _, statErr := os.Stat(filepath.Join(geminiDir, "oauth_creds.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected antigravity credential file removed, stat err = %v", statErr)
	}
}

func TestAgentEngineCLICheckUpdateSanitizesUpstreamError(t *testing.T) {
	engines := agentruntime.NewEnginesWithOptions(agentruntime.EnginesOptions{
		CLIManager: &agentruntime.CLIManagerOptions{
			Dir:    t.TempDir(),
			GOOS:   "linux",
			GOARCH: "amd64",
			Sources: map[config.RuntimeID]agentruntime.CLISource{
				config.RuntimeCodex: fakeCLISource{
					err: errors.New("GET https://internal-mirror.secret.local/token=abc: dial tcp 10.0.0.1:443: i/o timeout"),
				},
			},
			AutoUpdateDelay: time.Hour,
		},
		AuthManager: &agentruntime.AuthManagerOptions{Enabled: func() bool { return false }},
	})
	srv := startLANTestServer(t, engines)

	code, raw := srv.doLAN(t, http.MethodPost, "/api/agent-runtimes/codex/cli/check-update", "{}", true)
	obj := decodeErrBody(t, raw)
	if code != http.StatusServiceUnavailable || obj["code"] != "agentRuntime.connectionFailed" {
		t.Fatalf("failed check-update = (%d, %v), want 503 agentRuntime.connectionFailed", code, obj)
	}
	if bytes.Contains(raw, []byte("internal-mirror.secret.local")) || bytes.Contains(raw, []byte("10.0.0.1")) {
		t.Fatalf("CLI check-update leaked upstream network detail: %s", string(raw))
	}
	if details, ok := obj["details"].(map[string]any); ok {
		if _, hasRawDetail := details["detail"]; hasRawDetail {
			t.Fatalf("CLI check-update must not expose raw detail: %s", string(raw))
		}
	}
}
