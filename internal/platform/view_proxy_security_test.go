package platform

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveBackendViewPathRejectsTraversal(t *testing.T) {
	valid := []struct {
		base     string
		relative string
		rawPath  string
		want     string
	}{
		{base: "/", relative: "", rawPath: "/views/stage/", want: "/"},
		{base: "/", relative: "increment", rawPath: "/views/stage/increment", want: "/increment"},
		{base: "/ui", relative: "", rawPath: "/views/stage/", want: "/ui"},
		{base: "/ui", relative: "panel/state", rawPath: "/views/stage/panel/state", want: "/ui/panel/state"},
	}
	for _, tc := range valid {
		got, err := resolveBackendViewPath(tc.base, tc.relative, tc.rawPath)
		if err != nil || got != tc.want {
			t.Fatalf("resolveBackendViewPath(%q, %q, %q) = (%q, %v), want (%q, nil)", tc.base, tc.relative, tc.rawPath, got, err, tc.want)
		}
	}

	rejected := []struct {
		base     string
		relative string
		rawPath  string
	}{
		{base: "/ui", relative: "../tools/secret", rawPath: "/views/stage/../tools/secret"},
		{base: "/", relative: "../../tools/secret", rawPath: "/views/stage/../../tools/secret"},
		{base: "/ui", relative: "..", rawPath: "/views/stage/.."},
		{base: "/ui", relative: ".", rawPath: "/views/stage/."},
		{base: "/ui", relative: `panel\..\tools`, rawPath: `/views/stage/panel\..\tools`},
		{base: "/ui", relative: "panel/../tools", rawPath: "/views/stage/panel/%2e%2e/tools"},
		{base: "/ui", relative: "panel/tools", rawPath: "/views/stage/panel%2f..%2ftools"},
		{base: "/ui", relative: "panel/tools", rawPath: "/views/stage/panel%5ctools"},
		{base: "/ui", relative: "panel\x00tools", rawPath: "/views/stage/panel%00tools"},
	}
	for _, tc := range rejected {
		if got, err := resolveBackendViewPath(tc.base, tc.relative, tc.rawPath); err == nil {
			t.Fatalf("resolveBackendViewPath(%q, %q, %q) unexpectedly succeeded with %q", tc.base, tc.relative, tc.rawPath, got)
		}
	}
}

func TestServeViewBackendProxyBlocksTraversalWithHostBearerToken(t *testing.T) {
	var leakedToolCalls atomic.Int32
	var allowedViewCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tools/") {
			leakedToolCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/ui/panel" && r.Header.Get("Authorization") == "Bearer secret-host-token" {
			allowedViewCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	var stageView View
	stageView.ID = "stage"
	stageView.Source.Kind = "backend"
	stageView.Source.Path = "/ui"

	rt := &Runtime{
		ctx:          context.Background(),
		baseURL:      "http://example.com",
		parentOrigin: "http://127.0.0.1:15173",
		status:       "running",
		owner: &activation{
			release: Release{
				Manifest: Manifest{
					Views: []View{stageView},
				},
			},
			process: &backendProcess{
				baseURL: upstream.URL,
				token:   "secret-host-token",
			},
		},
	}

	for _, target := range []string{
		"/views/stage/../../tools/invoke",
		"/views/stage/../tools/invoke",
		"/views/stage/%2e%2e/tools/invoke",
		`/views/stage/..\tools\invoke`,
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rt.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d, want %d", target, rec.Code, http.StatusBadRequest)
		}
	}
	if leakedToolCalls.Load() != 0 {
		t.Fatalf("traversal reached backend /tools endpoint %d times", leakedToolCalls.Load())
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/views/stage/panel", nil)
	rt.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" || allowedViewCalls.Load() != 1 {
		t.Fatalf("valid backend view request failed: status=%d body=%q calls=%d", rec.Code, rec.Body.String(), allowedViewCalls.Load())
	}
}

type flushCallbackRecorder struct {
	*httptest.ResponseRecorder
	onFlush func()
}

func (r *flushCallbackRecorder) Flush() {
	r.ResponseRecorder.Flush()
	if r.onFlush != nil {
		fn := r.onFlush
		r.onFlush = nil
		fn()
	}
}

func TestServeAgentEventsFlushesTerminalResultWhenAlreadyDone(t *testing.T) {
	execution := &agentExecution{
		done:       make(chan struct{}),
		generation: "gen-1",
		receipt:    agentReceipt{Result: RunResult{Status: "running"}},
	}

	rec := &flushCallbackRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		onFlush: func() {
			execution.mu.Lock()
			execution.next++
			execution.receipt.Result = RunResult{Status: "completed", Text: "terminal-output"}
			execution.events = append(execution.events, streamEvent{
				sequence: execution.next,
				Kind:     "result",
				Data:     execution.receipt.Result,
			})
			close(execution.done)
			execution.mu.Unlock()
		},
	}
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(context.Background())
	serveAgentEvents(rec, req, execution, RunResult{})

	body := rec.Body.String()
	if !strings.Contains(body, "event: snapshot") || !strings.Contains(body, "event: result") || !strings.Contains(body, "terminal-output") {
		t.Fatalf("SSE stream dropped terminal result frame:\n%s", body)
	}
}

func TestManagerInstancesSkipsIncompleteInstanceDirectory(t *testing.T) {
	m, projectID := testManager(t)
	release := testInstall(t, m, testCandidate(t, m, projectID, "static", "test.static", Game))
	valid, err := m.CreateInstance(CreateInstance{GameID: release.Manifest.ID, ReleaseID: release.Ref.ReleaseID, Title: "Valid"})
	if err != nil {
		t.Fatal(err)
	}
	brokenDir := m.instancePath(release.Manifest.ID, "broken-incomplete")
	if err := os.MkdirAll(brokenDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := m.Instance(valid.ID)
	if err != nil || got.ID != valid.ID {
		t.Fatalf("Instance(%q) = (%+v, %v), want valid instance", valid.ID, got, err)
	}
	list, err := m.Instances()
	if err != nil {
		t.Fatalf("Instances() failed on incomplete sibling dir: %v", err)
	}
	if len(list) != 1 || list[0].ID != valid.ID {
		t.Fatalf("Instances() = %+v, want only %q", list, valid.ID)
	}
}

func TestServeStaticViewServesModuleScriptsWithJavaScriptMimeType(t *testing.T) {
	m, projectID := testManager(t)
	release := testInstall(t, m, testCandidate(t, m, projectID, "static", "test.static.mime", Game))
	rt := &Runtime{
		ctx:          context.Background(),
		manager:      m,
		baseURL:      "http://example.com",
		parentOrigin: "http://127.0.0.1:15173",
		status:       "running",
		owner:        &activation{release: release},
	}

	viewID := release.Manifest.Views[0].ID
	for _, tc := range []struct {
		target string
		want   string
	}{
		{target: "/views/" + viewID + "/", want: "text/html"},
		{target: "/views/" + viewID + "/server.mjs", want: "text/javascript"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		rt.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200: %s", tc.target, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.want) {
			t.Fatalf("GET %s Content-Type = %q, want prefix %q", tc.target, got, tc.want)
		}
	}
}
