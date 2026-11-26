package agentruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"denova/config"
)

const (
	AuthModeDeviceCode = "device_code"
	AuthModeCodePaste  = "code_paste"

	AuthStatusStarting     = "starting"
	AuthStatusAwaitingUser = "awaiting_user"
	AuthStatusCompleting   = "completing"
	AuthStatusSucceeded    = "succeeded"
	AuthStatusFailed       = "failed"
	AuthStatusCancelled    = "cancelled"

	defaultAuthSessionTTL    = 10 * time.Minute
	maxAuthInputBytes        = 4096
	maxAuthOutputBufferBytes = 128 * 1024
	authProbeTimeout         = 30 * time.Second
	authCallbackTimeout      = 10 * time.Second
	authPartialFlushDelay    = 150 * time.Millisecond
	maxRetainedAuthSessions  = 64
)

var (
	ErrAuthUnsupported         = errors.New("interactive runtime authentication is unavailable")
	ErrAuthSessionNotFound     = errors.New("authentication session not found")
	ErrAuthSessionInvalidState = errors.New("authentication session cannot accept input in its current state")
	ErrAuthInputInvalid        = errors.New("invalid authentication input")
)

// AuthStartOptions configures interactive login for a runtime engine.
// Claude accepts "", "claudeai", or "console"; other engines require an empty Method.
type AuthStartOptions struct {
	Method string `json:"method,omitempty"`
}

// AuthSessionSnapshot is the memory-only API projection of an interactive OAuth
// login session. Sensitive fields are returned to the authenticated browser
// session only and are never logged or persisted.
type AuthSessionSnapshot struct {
	SessionID   string            `json:"session_id"`
	Engine      config.RuntimeID  `json:"engine"`
	Mode        string            `json:"mode"`
	Status      string            `json:"status"`
	AuthURL     string            `json:"auth_url,omitempty"`
	UserCode    string            `json:"user_code,omitempty"`
	HintKey     string            `json:"hint_key,omitempty"`
	ErrorKey    string            `json:"error_key,omitempty"`
	ExpiresAt   string            `json:"expires_at"`
	EngineState *EngineDescriptor `json:"engine_state,omitempty"`
}

// AuthManagerOptions injects environment gating, engine discovery/probing, and
// test hooks into AuthManager.
type AuthManagerOptions struct {
	Enabled        func() bool
	TTL            time.Duration
	Probe          func(context.Context, config.RuntimeID) (EngineDescriptor, error)
	IsBusy         func(config.RuntimeID) bool
	CloseIdle      func(config.RuntimeID) error
	Discover       func(config.RuntimeID) (executable string, args []string, extraEnv []string, err error)
	CallbackClient *http.Client
	UserHomeDir    func() (string, error)
	Logger         *slog.Logger
	PreferPipes    bool
}

// AuthManager owns short-lived interactive CLI OAuth login sessions and logout
// operations across Codex, Claude Code, and Antigravity. At most one active
// session exists per engine, and Close terminates and reaps every subprocess.
type AuthManager struct {
	enabled        func() bool
	ttl            time.Duration
	probe          func(context.Context, config.RuntimeID) (EngineDescriptor, error)
	isBusy         func(config.RuntimeID) bool
	closeIdle      func(config.RuntimeID) error
	discover       func(config.RuntimeID) (string, []string, []string, error)
	callbackClient *http.Client
	userHomeDir    func() (string, error)
	logger         *slog.Logger
	preferPipes    bool

	baseCtx    context.Context
	baseCancel context.CancelFunc

	// Lock hierarchy: lifecycleMu -> mu -> session.mu.
	lifecycleMu sync.Mutex
	mu          sync.Mutex
	closed      bool
	sessions    map[config.RuntimeID]*authSession
	byID        map[string]*authSession
	wg          sync.WaitGroup
}

type authSession struct {
	id        string
	engine    config.RuntimeID
	mode      string
	hintKey   string
	expiresAt time.Time

	ctx    context.Context
	cancel context.CancelFunc
	proc   *authProcess

	mu            sync.Mutex
	status        string
	authURL       string
	urlPriority   int
	userCode      string
	redirectPort  int
	errorKey      string
	engineState   *EngineDescriptor
	submitting    bool
	processExited bool
	done          bool
	outputBuf     bytes.Buffer
}

func NewAuthManager(options AuthManagerOptions) *AuthManager {
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultAuthSessionTTL
	}
	discover := options.Discover
	if discover == nil {
		discover = defaultDiscoverAuthCLI
	}
	userHomeDir := options.UserHomeDir
	if userHomeDir == nil {
		userHomeDir = os.UserHomeDir
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseCtx, baseCancel := context.WithCancel(context.Background())
	return &AuthManager{
		enabled:        options.Enabled,
		ttl:            ttl,
		probe:          options.Probe,
		isBusy:         options.IsBusy,
		closeIdle:      options.CloseIdle,
		discover:       discover,
		callbackClient: newAuthCallbackClient(options.CallbackClient),
		userHomeDir:    userHomeDir,
		logger:         logger,
		preferPipes:    options.PreferPipes,
		baseCtx:        baseCtx,
		baseCancel:     baseCancel,
		sessions:       make(map[config.RuntimeID]*authSession),
		byID:           make(map[string]*authSession),
	}
}

func newAuthCallbackClient(custom *http.Client) *http.Client {
	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if custom != nil {
		cloned := *custom
		cloned.CheckRedirect = noRedirect
		if cloned.Timeout <= 0 {
			cloned.Timeout = authCallbackTimeout
		}
		return &cloned
	}
	return &http.Client{
		Timeout:       authCallbackTimeout,
		CheckRedirect: noRedirect,
		Transport: &http.Transport{
			// Never send loopback OAuth callbacks through an external HTTP proxy.
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
		},
	}
}

func (m *AuthManager) Supported(id config.RuntimeID) bool {
	if m == nil || m.enabled == nil || !m.enabled() {
		return false
	}
	return isAuthSupportedEngine(id)
}

func (m *AuthManager) Start(ctx context.Context, id config.RuntimeID, opts AuthStartOptions) (AuthSessionSnapshot, error) {
	if !m.Supported(id) {
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	if err := ctx.Err(); err != nil {
		return AuthSessionSnapshot{}, err
	}
	launch, err := buildAuthLaunchSpec(id, opts)
	if err != nil {
		return AuthSessionSnapshot{}, err
	}

	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	m.mu.Unlock()

	executable, prefixArgs, discoverEnv, err := m.discover(id)
	if err != nil {
		return AuthSessionSnapshot{}, err
	}
	if strings.TrimSpace(executable) == "" {
		return AuthSessionSnapshot{}, ErrEngineNotInstalled
	}

	sessionID, err := generateAuthSessionID()
	if err != nil {
		return AuthSessionSnapshot{}, fmt.Errorf("generate auth session id: %w", err)
	}

	// Cancel any existing session for this engine before spawning the replacement.
	m.cancelActiveForEngine(id)

	cmdArgs := append(append([]string(nil), prefixArgs...), launch.Args...)
	env := buildAuthProcessEnv(ctx, launch.ExtraEnv, discoverEnv)

	proc, err := startAuthProcess(authProcessSpec{
		Executable:  executable,
		Args:        cmdArgs,
		Env:         env,
		UsePTY:      launch.UsePTY,
		PreferPipes: m.preferPipes,
		Logger:      m.logger,
		Engine:      id,
		SessionID:   sessionID,
	})
	if err != nil {
		return AuthSessionSnapshot{}, err
	}

	// Session lifetime is decoupled from the HTTP request context so the CLI
	// remains alive after Start returns, bounded by the manager TTL and Close().
	sessionCtx, sessionCancel := context.WithTimeout(m.baseCtx, m.ttl)
	session := &authSession{
		id:        sessionID,
		engine:    id,
		mode:      launch.Mode,
		hintKey:   launch.HintKey,
		expiresAt: time.Now().Add(m.ttl).UTC(),
		ctx:       sessionCtx,
		cancel:    sessionCancel,
		proc:      proc,
		status:    AuthStatusStarting,
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		sessionCancel()
		proc.Terminate()
		_ = proc.Wait()
		proc.Cleanup()
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	m.pruneExpiredLocked(time.Now())
	m.sessions[id] = session
	m.byID[sessionID] = session
	m.wg.Add(2)
	m.mu.Unlock()

	outputDone := make(chan struct{})
	go m.pumpSessionOutput(session, outputDone)
	go m.watchSessionProcess(session, outputDone)

	m.logger.Info("[agent-runtime] started interactive auth session",
		"engine", id, "session_id", sessionID, "mode", launch.Mode)

	return session.snapshot(), nil
}

func (m *AuthManager) Get(id config.RuntimeID, sessionID string) (AuthSessionSnapshot, error) {
	if !m.Supported(id) {
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	session, err := m.lookupSession(id, sessionID)
	if err != nil {
		return AuthSessionSnapshot{}, err
	}
	return session.snapshot(), nil
}

func (m *AuthManager) Submit(ctx context.Context, id config.RuntimeID, sessionID, input string) (AuthSessionSnapshot, error) {
	if !m.Supported(id) {
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	if err := ctx.Err(); err != nil {
		return AuthSessionSnapshot{}, err
	}
	session, err := m.lookupSession(id, sessionID)
	if err != nil {
		return AuthSessionSnapshot{}, err
	}

	session.mu.Lock()
	if session.mode != AuthModeCodePaste || session.status != AuthStatusAwaitingUser || session.submitting {
		session.mu.Unlock()
		return AuthSessionSnapshot{}, ErrAuthSessionInvalidState
	}
	expectedPort := session.redirectPort
	session.mu.Unlock()

	normalized, replayURL, isCallback, err := validateAuthInput(input, expectedPort)
	if err != nil {
		return AuthSessionSnapshot{}, ErrAuthInputInvalid
	}

	session.mu.Lock()
	if session.status != AuthStatusAwaitingUser || session.submitting {
		session.mu.Unlock()
		return AuthSessionSnapshot{}, ErrAuthSessionInvalidState
	}
	session.submitting = true
	session.status = AuthStatusCompleting
	proc := session.proc
	session.mu.Unlock()

	var submitErr error
	if isCallback {
		submitErr = m.replayLoopbackCallback(ctx, session, replayURL)
	} else {
		if writeErr := proc.WriteInput(normalized); writeErr != nil {
			submitErr = errors.New("failed to deliver authentication code")
		}
	}

	session.mu.Lock()
	session.submitting = false
	if submitErr != nil {
		if session.processExited {
			// The CLI exited while handling the callback/code (e.g., closed its
			// loopback server immediately after persisting credentials). Keep
			// the completing/terminal state instead of rolling back onto a dead process.
			snap := session.snapshotLocked()
			session.mu.Unlock()
			return snap, nil
		}
		if session.status == AuthStatusCompleting {
			session.status = AuthStatusAwaitingUser
		}
		session.mu.Unlock()
		m.logger.Warn("[agent-runtime] auth submission failed",
			"engine", id, "session_id", sessionID, "callback_replay", isCallback)
		return AuthSessionSnapshot{}, submitErr
	}
	snap := session.snapshotLocked()
	session.mu.Unlock()

	m.logger.Info("[agent-runtime] submitted interactive auth input",
		"engine", id, "session_id", sessionID, "callback_replay", isCallback)
	return snap, nil
}

func (m *AuthManager) Cancel(id config.RuntimeID, sessionID string) (AuthSessionSnapshot, error) {
	if !m.Supported(id) {
		return AuthSessionSnapshot{}, ErrAuthUnsupported
	}
	session, err := m.lookupSession(id, sessionID)
	if err != nil {
		return AuthSessionSnapshot{}, err
	}
	session.markCancelled()
	m.logger.Info("[agent-runtime] cancelled interactive auth session",
		"engine", id, "session_id", sessionID)
	return session.snapshot(), nil
}

func (m *AuthManager) Logout(ctx context.Context, id config.RuntimeID) (EngineDescriptor, error) {
	if !m.Supported(id) {
		return EngineDescriptor{}, ErrAuthUnsupported
	}
	if err := ctx.Err(); err != nil {
		return EngineDescriptor{}, err
	}
	if m.isBusy != nil && m.isBusy(id) {
		return EngineDescriptor{}, ErrOperationActive
	}

	m.lifecycleMu.Lock()
	// Re-check busy under lifecycleMu before mutating credentials.
	if m.isBusy != nil && m.isBusy(id) {
		m.lifecycleMu.Unlock()
		return EngineDescriptor{}, ErrOperationActive
	}
	m.cancelActiveForEngine(id)

	var logoutErr error
	switch id {
	case config.RuntimeCodex:
		logoutErr = m.runCLILogout(ctx, id, []string{"logout"})
	case config.RuntimeClaude:
		logoutErr = m.runCLILogout(ctx, id, []string{"auth", "logout"})
	case config.RuntimeAntigravity:
		home, err := m.userHomeDir()
		if err != nil {
			logoutErr = errors.New("resolve home directory for Antigravity logout")
		} else {
			logoutErr = clearAntigravityCredentials(home)
		}
	default:
		logoutErr = ErrAuthUnsupported
	}
	if logoutErr != nil {
		m.lifecycleMu.Unlock()
		m.logger.Warn("[agent-runtime] interactive logout failed", "engine", id)
		return EngineDescriptor{}, logoutErr
	}
	if m.closeIdle != nil {
		if err := m.closeIdle(id); err != nil {
			m.lifecycleMu.Unlock()
			if errors.Is(err, ErrOperationActive) {
				return EngineDescriptor{}, ErrOperationActive
			}
			m.logger.Warn("[agent-runtime] failed to close idle engine connection after logout", "engine", id)
			return EngineDescriptor{}, err
		}
	}
	m.lifecycleMu.Unlock()

	m.logger.Info("[agent-runtime] logged out runtime engine", "engine", id)
	if m.probe != nil {
		return m.probe(ctx, id)
	}
	return engineDescriptor(id), nil
}

func (m *AuthManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.wg.Wait()
		return
	}
	m.closed = true
	active := make([]*authSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s != nil {
			active = append(active, s)
		}
	}
	m.mu.Unlock()

	for _, s := range active {
		s.markCancelled()
	}
	m.baseCancel()
	m.wg.Wait()
}

func (m *AuthManager) runCLILogout(ctx context.Context, id config.RuntimeID, subArgs []string) error {
	executable, prefixArgs, discoverEnv, err := m.discover(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(executable) == "" {
		return ErrEngineNotInstalled
	}
	args := append(append([]string(nil), prefixArgs...), subArgs...)
	env := buildAuthProcessEnv(ctx, []string{"BROWSER=false", "NO_BROWSER=1"}, discoverEnv)
	return runLogoutCommand(ctx, id, executable, args, env)
}

func (m *AuthManager) cancelActiveForEngine(id config.RuntimeID) {
	m.mu.Lock()
	prev := m.sessions[id]
	m.mu.Unlock()
	if prev != nil {
		prev.markCancelled()
	}
}

func (m *AuthManager) lookupSession(id config.RuntimeID, sessionID string) (*authSession, error) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneExpiredLocked(now)
	session := m.byID[sessionID]
	if session == nil || session.engine != id {
		return nil, ErrAuthSessionNotFound
	}
	if now.After(session.expiresAt) {
		delete(m.byID, sessionID)
		if m.sessions[id] == session {
			delete(m.sessions, id)
		}
		return nil, ErrAuthSessionNotFound
	}
	return session, nil
}

func (m *AuthManager) pruneExpiredLocked(now time.Time) {
	for sid, s := range m.byID {
		if now.After(s.expiresAt) {
			delete(m.byID, sid)
			if m.sessions[s.engine] == s {
				delete(m.sessions, s.engine)
			}
		}
	}
	if len(m.byID) <= maxRetainedAuthSessions {
		return
	}
	// Evict finished non-current sessions first to bound memory usage.
	for sid, s := range m.byID {
		if len(m.byID) <= maxRetainedAuthSessions {
			break
		}
		if m.sessions[s.engine] == s {
			continue
		}
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		if done {
			delete(m.byID, sid)
		}
	}
}

func (m *AuthManager) pumpSessionOutput(session *authSession, outputDone chan<- struct{}) {
	defer m.wg.Done()
	defer close(outputDone)
	var flushTimer *time.Timer
	var flushMu sync.Mutex
	var timerWG sync.WaitGroup
	stopTimer := func() {
		flushMu.Lock()
		t := flushTimer
		flushTimer = nil
		flushMu.Unlock()
		if t != nil && t.Stop() {
			timerWG.Done()
		}
	}
	defer func() {
		stopTimer()
		timerWG.Wait()
		if recovered := recover(); recovered != nil {
			m.logger.Error("[agent-runtime] panic in auth output pump",
				"engine", session.engine, "session_id", session.id, "stack", string(debug.Stack()))
			session.fail("agentRuntime.auth.failed")
			session.cancel()
		}
	}()

	buf := make([]byte, 4096)
	for {
		n, err := session.proc.Read(buf)
		if n > 0 {
			becameReady := session.appendOutput(buf[:n], false)
			if becameReady {
				stopTimer()
				m.logger.Info("[agent-runtime] auth session awaiting user",
					"engine", session.engine, "session_id", session.id, "mode", session.mode)
			} else if session.needsPartialFlush() {
				stopTimer()
				timerWG.Add(1)
				flushMu.Lock()
				flushTimer = time.AfterFunc(authPartialFlushDelay, func() {
					defer timerWG.Done()
					defer func() { _ = recover() }()
					if session.parseBuffered(true) {
						m.logger.Info("[agent-runtime] auth session awaiting user",
							"engine", session.engine, "session_id", session.id, "mode", session.mode)
					}
				})
				flushMu.Unlock()
			}
		}
		if err != nil {
			stopTimer()
			session.parseBuffered(true)
			return
		}
	}
}

func (m *AuthManager) watchSessionProcess(session *authSession, outputDone <-chan struct{}) {
	defer m.wg.Done()
	defer func() {
		if recovered := recover(); recovered != nil {
			m.logger.Error("[agent-runtime] panic in auth process watcher",
				"engine", session.engine, "session_id", session.id, "stack", string(debug.Stack()))
			session.fail("agentRuntime.auth.failed")
			session.proc.Terminate()
			session.proc.Cleanup()
			session.cancel()
		}
	}()

	stopCancel := context.AfterFunc(session.ctx, func() {
		session.proc.Terminate()
	})
	waitErr := session.proc.Wait()
	stopCancel()
	session.mu.Lock()
	session.processExited = true
	session.mu.Unlock()

	// Drain any trailing prompt output before releasing handles and tempDir.
	select {
	case <-outputDone:
	case <-time.After(2 * time.Second):
		session.proc.CloseHandles()
		<-outputDone
	}
	session.proc.Cleanup()

	ctxErr := session.ctx.Err()
	session.cancel()

	session.mu.Lock()
	if session.status == AuthStatusCancelled {
		session.done = true
		session.mu.Unlock()
		return
	}
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		session.status = AuthStatusFailed
		session.errorKey = "agentRuntime.auth.expired"
		session.done = true
		session.mu.Unlock()
		m.logger.Warn("[agent-runtime] auth session timed out",
			"engine", session.engine, "session_id", session.id)
		return
	}
	if ctxErr != nil {
		session.status = AuthStatusCancelled
		session.done = true
		session.mu.Unlock()
		return
	}
	if waitErr != nil {
		session.status = AuthStatusFailed
		session.errorKey = "agentRuntime.auth.failed"
		session.done = true
		session.mu.Unlock()
		m.logger.Warn("[agent-runtime] auth CLI exited with failure",
			"engine", session.engine, "session_id", session.id)
		return
	}
	session.status = AuthStatusCompleting
	session.mu.Unlock()

	if m.probe == nil {
		session.mu.Lock()
		if session.status != AuthStatusCancelled {
			session.status = AuthStatusSucceeded
			session.errorKey = ""
		}
		session.done = true
		session.mu.Unlock()
		m.logger.Info("[agent-runtime] auth session succeeded",
			"engine", session.engine, "session_id", session.id)
		return
	}

	probeCtx, probeCancel := context.WithTimeout(m.baseCtx, authProbeTimeout)
	desc, probeErr := m.probe(probeCtx, session.engine)
	probeCancel()

	session.mu.Lock()
	defer session.mu.Unlock()
	session.done = true
	if session.status == AuthStatusCancelled || m.baseCtx.Err() != nil {
		session.status = AuthStatusCancelled
		return
	}
	if probeErr != nil {
		session.status = AuthStatusFailed
		session.errorKey = "agentRuntime.auth.failed"
		m.logger.Warn("[agent-runtime] post-login engine probe failed",
			"engine", session.engine, "session_id", session.id)
		return
	}
	descCopy := desc
	session.engineState = &descCopy
	if desc.Status != "" && desc.Status != "ready" {
		session.status = AuthStatusFailed
		session.errorKey = "agentRuntime.auth.failed"
		m.logger.Warn("[agent-runtime] post-login engine not ready",
			"engine", session.engine, "session_id", session.id, "engine_status", desc.Status)
		return
	}
	session.status = AuthStatusSucceeded
	session.errorKey = ""
	m.logger.Info("[agent-runtime] auth session succeeded",
		"engine", session.engine, "session_id", session.id)
}

func (s *authSession) appendOutput(chunk []byte, includeTail bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outputBuf.Len() < maxAuthOutputBufferBytes {
		remaining := maxAuthOutputBufferBytes - s.outputBuf.Len()
		if len(chunk) > remaining {
			chunk = chunk[:remaining]
		}
		_, _ = s.outputBuf.Write(chunk)
	}
	return s.parseLocked(includeTail)
}

func (s *authSession) parseBuffered(includeTail bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parseLocked(includeTail)
}

func (s *authSession) parseLocked(includeTail bool) bool {
	if s.status != AuthStatusStarting && s.status != AuthStatusAwaitingUser {
		return false
	}
	update := parseAuthPromptRaw(s.engine, s.outputBuf.String(), includeTail)
	if s.status == AuthStatusAwaitingUser {
		// Upgrade URL/redirect port if a higher-priority OAuth link arrives in a
		// later chunk after a low-priority banner URL triggered partial-flush readiness.
		if update.AuthURL != "" && update.URLPriority > s.urlPriority {
			s.authURL = update.AuthURL
			s.urlPriority = update.URLPriority
			if update.RedirectPort > 0 {
				s.redirectPort = update.RedirectPort
			}
			if update.UserCode != "" {
				s.userCode = update.UserCode
			}
		}
		return false
	}
	if update.AuthURL != "" && update.URLPriority >= s.urlPriority {
		s.authURL = update.AuthURL
		s.urlPriority = update.URLPriority
	}
	if update.UserCode != "" {
		s.userCode = update.UserCode
	}
	if update.RedirectPort > 0 {
		s.redirectPort = update.RedirectPort
	}
	if update.Ready {
		s.status = AuthStatusAwaitingUser
		return true
	}
	return false
}

func (s *authSession) needsPartialFlush() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status == AuthStatusStarting || (s.status == AuthStatusAwaitingUser && s.urlPriority < 2)
}

func (s *authSession) markCancelled() {
	s.mu.Lock()
	if s.status != AuthStatusSucceeded && s.status != AuthStatusFailed {
		s.status = AuthStatusCancelled
	}
	s.mu.Unlock()
	s.cancel()
	if s.proc != nil {
		s.proc.Terminate()
	}
}

func (s *authSession) fail(errorKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == AuthStatusCancelled || s.status == AuthStatusSucceeded {
		return
	}
	s.status = AuthStatusFailed
	s.errorKey = errorKey
}

func (s *authSession) snapshot() AuthSessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *authSession) snapshotLocked() AuthSessionSnapshot {
	snap := AuthSessionSnapshot{
		SessionID: s.id,
		Engine:    s.engine,
		Mode:      s.mode,
		Status:    s.status,
		AuthURL:   s.authURL,
		UserCode:  s.userCode,
		HintKey:   s.hintKey,
		ErrorKey:  s.errorKey,
		ExpiresAt: s.expiresAt.Format(time.RFC3339),
	}
	if s.engineState != nil {
		copyState := *s.engineState
		snap.EngineState = &copyState
	}
	return snap
}
