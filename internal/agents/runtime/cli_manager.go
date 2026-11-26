package agentruntime

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"denova/config"
	"denova/internal/hostruntime"
)

var (
	// ErrCLIManagementUnsupported reports that managed CLI operations are disabled
	// for the current environment or requested runtime.
	ErrCLIManagementUnsupported = errors.New("managed CLI updates are unavailable in this environment")
	// ErrCLIOperationBusy reports that a check, install, or update is already
	// running for the requested runtime.
	ErrCLIOperationBusy = errors.New("CLI install or update is already in progress")
)

// ManagedCLIStatus is a pure in-memory snapshot of one managed CLI binary's
// installation, update availability, and background download progress.
type ManagedCLIStatus struct {
	Managed          bool   `json:"managed"`
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	State            string `json:"state"` // "idle" | "checking" | "installing" | "updating" | "error"
	BytesDownloaded  int64  `json:"bytes_downloaded,omitempty"`
	TotalBytes       int64  `json:"total_bytes,omitempty"`
	LastCheckedAt    string `json:"last_checked_at,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	PendingReload    bool   `json:"pending_reload,omitempty"`
}

// CLIManagerOptions configures managed CLI installation and background updates.
// Management is enabled only when Dir is a non-empty absolute path and the
// target platform is linux/amd64 or linux/arm64.
type CLIManagerOptions struct {
	Dir            string
	GOOS           string
	GOARCH         string
	HTTPClient     *http.Client
	Sources        map[config.RuntimeID]CLISource
	SourceBaseURLs map[config.RuntimeID]string
	VerifyBinary   func(ctx context.Context, id config.RuntimeID, path string) (string, error)
	OnReload       func(ctx context.Context, id config.RuntimeID) (pendingReload bool, installedVersion string, err error)

	MaxDownloadBytes    int64
	MaxExtractBytes     int64
	AutoUpdateDelay     time.Duration
	AutoUpdateTick      time.Duration
	AutoUpdateInterval  time.Duration
	AutoUpdateRetryWait time.Duration
	Now                 func() time.Time
}

type managedCLIEntry struct {
	status             ManagedCLIStatus
	busy               bool
	pendingReloadSeq   uint64
	lastChecked        time.Time
	lastCheckFailedAt  time.Time
	lastInstallAttempt time.Time
	failedAutoVersion  string
}

// CLIManager owns download, checksum verification, safe extraction, atomic
// replacement, and background auto-update scheduling for managed Linux CLIs.
type CLIManager struct {
	enabled             bool
	dir                 string
	goos                string
	goarch              string
	httpClient          *http.Client
	sources             map[config.RuntimeID]CLISource
	verifyBinary        func(ctx context.Context, id config.RuntimeID, path string) (string, error)
	onReload            func(ctx context.Context, id config.RuntimeID) (pendingReload bool, installedVersion string, err error)
	maxDownloadBytes    int64
	maxExtractBytes     int64
	autoUpdateDelay     time.Duration
	autoUpdateTick      time.Duration
	autoUpdateInterval  time.Duration
	autoUpdateRetryWait time.Duration
	now                 func() time.Time

	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	entries map[config.RuntimeID]*managedCLIEntry
	wg      sync.WaitGroup
}

// NewCLIManager creates a manager for container-hosted external CLIs. It never
// spawns subprocesses or performs network requests during construction.
func NewCLIManager(options CLIManagerOptions) *CLIManager {
	goos := strings.TrimSpace(options.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(options.GOARCH)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	dir := strings.TrimSpace(options.Dir)
	enabled := isManagedDirValid(dir, goos) && goos == "linux" && (goarch == "amd64" || goarch == "arm64")
	if enabled {
		if filepath.IsAbs(dir) {
			dir = filepath.Clean(dir)
		} else {
			dir = path.Clean(dir)
		}
	} else {
		dir = ""
	}

	sources := defaultCLISources(options.SourceBaseURLs)
	for id, src := range options.Sources {
		if isManagedCLIEngine(id) && src != nil {
			sources[id] = src
		}
	}

	verifyFn := options.VerifyBinary
	if verifyFn == nil {
		verifyFn = defaultVerifyBinary
	}

	maxDownload := options.MaxDownloadBytes
	if maxDownload <= 0 {
		maxDownload = defaultMaxDownloadBytes
	}
	maxExtract := options.MaxExtractBytes
	if maxExtract <= 0 {
		maxExtract = defaultMaxExtractBytes
	}

	delay := options.AutoUpdateDelay
	if delay <= 0 {
		delay = defaultAutoUpdateDelay
	}
	tick := options.AutoUpdateTick
	if tick <= 0 {
		tick = defaultAutoUpdateTick
	}
	interval := options.AutoUpdateInterval
	if interval <= 0 {
		interval = defaultAutoUpdateInterval
	}
	retryWait := options.AutoUpdateRetryWait
	if retryWait <= 0 {
		retryWait = defaultAutoUpdateRetryWait
	}
	nowFn := options.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	baseCtx, baseCancel := context.WithCancel(context.Background())
	entries := make(map[config.RuntimeID]*managedCLIEntry, len(managedCLIEngines))
	for _, id := range managedCLIEngines {
		entries[id] = &managedCLIEntry{
			status: ManagedCLIStatus{
				Managed: true,
				State:   "idle",
			},
		}
	}

	return &CLIManager{
		enabled:             enabled,
		dir:                 dir,
		goos:                goos,
		goarch:              goarch,
		httpClient:          buildCLIHTTPClient(options.HTTPClient),
		sources:             sources,
		verifyBinary:        verifyFn,
		onReload:            options.OnReload,
		maxDownloadBytes:    maxDownload,
		maxExtractBytes:     maxExtract,
		autoUpdateDelay:     delay,
		autoUpdateTick:      tick,
		autoUpdateInterval:  interval,
		autoUpdateRetryWait: retryWait,
		now:                 nowFn,
		baseCtx:             baseCtx,
		baseCancel:          baseCancel,
		entries:             entries,
	}
}

func isManagedDirValid(dir, goos string) bool {
	if dir == "" || strings.ContainsRune(dir, 0) {
		return false
	}
	if filepath.IsAbs(dir) {
		return true
	}
	// Allow Linux POSIX absolute paths when GOOS is overridden in cross-platform tests.
	return goos == "linux" && strings.HasPrefix(dir, "/")
}

func buildCLIHTTPClient(provided *http.Client) *http.Client {
	if provided != nil {
		cloned := *provided
		prevCheck := cloned.CheckRedirect
		cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if err := enforceHTTPSRedirect(req, via); err != nil {
				return err
			}
			if prevCheck != nil {
				return prevCheck(req, via)
			}
			return nil
		}
		return &cloned
	}
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.Proxy = hostruntime.NewHTTPProxy()
	transport.ResponseHeaderTimeout = cliManifestTimeout
	return &http.Client{
		Transport:     transport,
		CheckRedirect: enforceHTTPSRedirect,
	}
}

func enforceHTTPSRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL == nil || !strings.EqualFold(req.URL.Scheme, "https") {
		return errors.New("refusing redirect to non-https URL")
	}
	return nil
}

// Enabled reports whether managed CLI installation and updates are active.
func (m *CLIManager) Enabled() bool {
	return m != nil && m.enabled
}

// Status returns a snapshot of the managed CLI state from memory without
// invoking subprocesses or network requests. Returns nil when disabled or
// when id is not a managed CLI.
func (m *CLIManager) Status(id config.RuntimeID) *ManagedCLIStatus {
	if !m.Enabled() || !isManagedCLIEngine(id) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return nil
	}
	snapshot := entry.status
	return &snapshot
}

// RecordInstalled synchronizes cached installation status and version discovered
// by explicit probes or startup warm-up.
func (m *CLIManager) RecordInstalled(id config.RuntimeID, installed bool, version string) {
	if !m.Enabled() || !isManagedCLIEngine(id) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return
	}
	entry.status.Installed = installed
	if !installed {
		entry.status.InstalledVersion = ""
		entry.status.PendingReload = false
	} else if trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v"); trimmed != "" {
		if normalized, err := normalizeSemver(trimmed); err == nil {
			entry.status.InstalledVersion = normalized
		} else {
			entry.status.InstalledVersion = trimmed
		}
	}
	entry.status.UpdateAvailable = computeUpdateAvailable(entry.status.Installed, entry.status.InstalledVersion, entry.status.LatestVersion)
}

// SetPendingReload updates whether a newly replaced binary is awaiting release
// of active engine leases before taking effect.
func (m *CLIManager) SetPendingReload(id config.RuntimeID, pending bool) {
	if !m.Enabled() || !isManagedCLIEngine(id) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.entries[id]; entry != nil {
		entry.pendingReloadSeq++
		entry.status.PendingReload = pending
	}
}

// CheckUpdate fetches the latest official release manifest for id and updates
// cached version and update-availability state.
func (m *CLIManager) CheckUpdate(ctx context.Context, id config.RuntimeID) (ManagedCLIStatus, error) {
	if !m.Enabled() || !isManagedCLIEngine(id) {
		return ManagedCLIStatus{}, ErrCLIManagementUnsupported
	}
	source := m.sources[id]
	if source == nil {
		return ManagedCLIStatus{}, ErrCLIManagementUnsupported
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ManagedCLIStatus{}, context.Canceled
	}
	entry := m.entries[id]
	if entry.busy {
		snapshot := entry.status
		m.mu.Unlock()
		return snapshot, ErrCLIOperationBusy
	}
	entry.busy = true
	entry.status.State = "checking"
	entry.status.LastError = ""
	m.mu.Unlock()

	completed := false
	defer func() {
		if !completed {
			m.mu.Lock()
			entry.busy = false
			entry.status.State = "error"
			entry.status.LastError = "unexpected error while checking CLI update"
			m.mu.Unlock()
		}
	}()

	checkCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(m.baseCtx, cancel)
	defer stopCancel()
	defer cancel()

	release, err := source.Resolve(checkCtx, m.httpClient, m.goarch)

	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	completed = true
	entry.busy = false
	if err != nil {
		entry.lastCheckFailedAt = now
		entry.status.State = "error"
		entry.status.LastError = sanitizeCLIError(err)
		slog.WarnContext(ctx, "[agent-runtime] managed CLI update check failed", "runtime", id, "error", err)
		return entry.status, err
	}
	entry.lastChecked = now
	entry.lastCheckFailedAt = time.Time{}
	entry.status.LastCheckedAt = now.Format(time.RFC3339)
	entry.status.LatestVersion = release.Version
	entry.status.UpdateAvailable = computeUpdateAvailable(entry.status.Installed, entry.status.InstalledVersion, release.Version)
	entry.status.State = "idle"
	entry.status.LastError = ""
	return entry.status, nil
}

// StartInstall begins an asynchronous download, verification, and atomic
// replacement for id, returning the initial status immediately.
func (m *CLIManager) StartInstall(id config.RuntimeID) (ManagedCLIStatus, error) {
	if !m.Enabled() || !isManagedCLIEngine(id) {
		return ManagedCLIStatus{}, ErrCLIManagementUnsupported
	}
	if m.sources[id] == nil {
		return ManagedCLIStatus{}, ErrCLIManagementUnsupported
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ManagedCLIStatus{}, context.Canceled
	}
	entry := m.entries[id]
	if entry.busy {
		snapshot := entry.status
		m.mu.Unlock()
		return snapshot, ErrCLIOperationBusy
	}
	initialState := "installing"
	if entry.status.Installed {
		initialState = "updating"
	}
	entry.busy = true
	entry.status.State = initialState
	entry.status.LastError = ""
	entry.status.BytesDownloaded = 0
	entry.status.TotalBytes = 0
	// Explicit manual trigger clears any prior automatic failure suppression.
	entry.failedAutoVersion = ""
	entry.lastInstallAttempt = m.now().UTC()
	snapshot := entry.status
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("[agent-runtime] panic during managed CLI install", "runtime", id, "panic", r)
				m.recordInstallFailure(id, false, "", errors.New("unexpected panic during CLI installation"))
			}
		}()
		m.runInstall(m.baseCtx, id, false, "")
	}()

	return snapshot, nil
}

// Close cancels active downloads and waits for all background CLI operations
// to finish cleanly.
func (m *CLIManager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.baseCancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *CLIManager) runInstall(ctx context.Context, id config.RuntimeID, isAuto bool, fallbackVersion string) {
	source := m.sources[id]
	release, err := source.Resolve(ctx, m.httpClient, m.goarch)
	if err != nil {
		m.recordInstallFailure(id, isAuto, fallbackVersion, err)
		return
	}

	now := m.now().UTC()
	m.mu.Lock()
	if entry := m.entries[id]; entry != nil {
		entry.lastChecked = now
		entry.lastCheckFailedAt = time.Time{}
		entry.status.LastCheckedAt = now.Format(time.RFC3339)
		entry.status.LatestVersion = release.Version
		if release.ExpectedSize > 0 {
			entry.status.TotalBytes = release.ExpectedSize
		}
	}
	m.mu.Unlock()

	verifiedVersion, err := m.installRelease(ctx, id, release)
	if err != nil {
		m.recordInstallFailure(id, isAuto, release.Version, err)
		return
	}

	var (
		pendingReload    bool
		installedVersion = verifiedVersion
		reloadErr        error
		seqBeforeReload  uint64
	)
	m.mu.Lock()
	if entry := m.entries[id]; entry != nil {
		seqBeforeReload = entry.pendingReloadSeq
	}
	m.mu.Unlock()
	if m.onReload != nil {
		var reloadedVersion string
		pendingReload, reloadedVersion, reloadErr = m.onReload(ctx, id)
		if reloadedVersion != "" {
			if normalized, normErr := normalizeSemver(reloadedVersion); normErr == nil {
				installedVersion = normalized
			} else {
				installedVersion = strings.TrimSpace(reloadedVersion)
			}
		}
		if reloadErr != nil {
			slog.WarnContext(ctx, "[agent-runtime] managed CLI reload callback failed", "runtime", id, "version", installedVersion, "error", reloadErr)
		}
	}

	m.mu.Lock()
	if entry := m.entries[id]; entry != nil {
		entry.busy = false
		entry.status.Installed = true
		entry.status.InstalledVersion = installedVersion
		// Preserve concurrent SetPendingReload transitions (e.g., an active
		// lease releasing and flushing pending reload right after onReload).
		if entry.pendingReloadSeq == seqBeforeReload {
			entry.status.PendingReload = pendingReload
		} else {
			pendingReload = entry.status.PendingReload
		}
		entry.status.BytesDownloaded = 0
		entry.status.TotalBytes = 0
		entry.status.UpdateAvailable = computeUpdateAvailable(true, installedVersion, entry.status.LatestVersion)
		if reloadErr != nil {
			entry.status.State = "error"
			entry.status.LastError = sanitizeCLIError(reloadErr)
		} else {
			entry.status.State = "idle"
			entry.status.LastError = ""
			entry.failedAutoVersion = ""
		}
	}
	m.mu.Unlock()
	if reloadErr == nil {
		slog.InfoContext(ctx, "[agent-runtime] managed CLI installed", "runtime", id, "version", installedVersion, "pending_reload", pendingReload, "auto", isAuto)
	}
}

func (m *CLIManager) recordInstallFailure(id config.RuntimeID, isAuto bool, attemptedVersion string, err error) {
	slog.Warn("[agent-runtime] managed CLI install failed", "runtime", id, "auto", isAuto, "version", attemptedVersion, "error", err)
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return
	}
	entry.busy = false
	entry.status.State = "error"
	entry.status.BytesDownloaded = 0
	entry.status.TotalBytes = 0
	entry.status.LastError = sanitizeCLIError(err)
	if isAuto && attemptedVersion != "" {
		entry.failedAutoVersion = attemptedVersion
	}
}

func (m *CLIManager) installRelease(ctx context.Context, id config.RuntimeID, release CLIRelease) (string, error) {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return "", fmt.Errorf("prepare managed CLI directory: %w", err)
	}
	if release.ExpectedSize > m.maxDownloadBytes {
		return "", errors.New("CLI download exceeds configured size limit")
	}

	downloadPath, err := m.downloadArtifact(ctx, id, release)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(downloadPath) }()

	stagePath := downloadPath
	if release.Archive == cliArchiveTarGz {
		stageFile, err := os.CreateTemp(m.dir, ".denova-cli-"+string(id)+"-stage-*")
		if err != nil {
			return "", fmt.Errorf("create staged binary temp file: %w", err)
		}
		stagePath = stageFile.Name()
		if err := stageFile.Close(); err != nil {
			_ = os.Remove(stagePath)
			return "", fmt.Errorf("close staged binary temp file: %w", err)
		}
		defer func() { _ = os.Remove(stagePath) }()

		matcher := archiveBinaryMatcher(id, m.goarch)
		if err := extractSingleBinaryFromTarGz(downloadPath, stagePath, matcher, m.maxExtractBytes); err != nil {
			return "", err
		}
		// Remove the downloaded archive promptly once extracted.
		_ = os.Remove(downloadPath)
	}

	if err := os.Chmod(stagePath, 0o755); err != nil {
		return "", fmt.Errorf("chmod staged binary: %w", err)
	}

	verifiedVersion, err := m.verifyBinary(ctx, id, stagePath)
	if err != nil {
		return "", err
	}

	targetPath := filepath.Join(m.dir, cliBinaryName(id))
	if err := os.Rename(stagePath, targetPath); err != nil {
		return "", fmt.Errorf("replace CLI binary atomically: %w", err)
	}
	return verifiedVersion, nil
}

func (m *CLIManager) downloadArtifact(ctx context.Context, id config.RuntimeID, release CLIRelease) (string, error) {
	if err := validateHTTPSURL(release.DownloadURL); err != nil {
		return "", err
	}
	hasher, err := newReleaseHasher(release.HashAlgo, release.Digest)
	if err != nil {
		return "", err
	}

	dlCtx, cancel := context.WithTimeout(ctx, cliDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, release.DownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "denova-cli-manager")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download CLI package: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download CLI package: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > m.maxDownloadBytes {
		return "", errors.New("CLI download exceeds configured size limit")
	}

	totalBytes := release.ExpectedSize
	if totalBytes <= 0 && resp.ContentLength > 0 {
		totalBytes = resp.ContentLength
	}
	m.updateDownloadProgress(id, 0, totalBytes)

	tmpFile, err := os.CreateTemp(m.dir, ".denova-cli-"+string(id)+"-download-*")
	if err != nil {
		return "", fmt.Errorf("create download temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	progress := &downloadProgressWriter{
		onProgress: func(downloaded int64) {
			m.updateDownloadProgress(id, downloaded, totalBytes)
		},
	}
	writer := io.MultiWriter(tmpFile, hasher, progress)
	written, copyErr := io.Copy(writer, io.LimitReader(resp.Body, m.maxDownloadBytes+1))
	if copyErr != nil {
		cleanup()
		return "", fmt.Errorf("stream CLI download: %w", copyErr)
	}
	if written > m.maxDownloadBytes {
		cleanup()
		return "", errors.New("CLI download exceeds configured size limit")
	}
	if written == 0 {
		cleanup()
		return "", errors.New("CLI download returned empty body")
	}
	if release.ExpectedSize > 0 && written != release.ExpectedSize {
		cleanup()
		return "", fmt.Errorf("downloaded size %d does not match manifest size %d", written, release.ExpectedSize)
	}
	progress.flush(written)

	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("sync downloaded CLI file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close downloaded CLI file: %w", err)
	}

	actualDigest := hasher.Sum(nil)
	if subtle.ConstantTimeCompare(actualDigest, release.Digest) != 1 {
		_ = os.Remove(tmpPath)
		return "", errors.New("downloaded CLI checksum mismatch")
	}
	return tmpPath, nil
}

func (m *CLIManager) updateDownloadProgress(id config.RuntimeID, downloaded, total int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.entries[id]; entry != nil {
		entry.status.BytesDownloaded = downloaded
		if total > 0 {
			entry.status.TotalBytes = total
		}
	}
}

type downloadProgressWriter struct {
	written    int64
	onProgress func(downloaded int64)
}

func (w *downloadProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.written += int64(n)
	if n > 0 {
		w.onProgress(w.written)
	}
	return n, nil
}

func (w *downloadProgressWriter) flush(total int64) {
	if total != w.written {
		w.written = total
		w.onProgress(total)
	}
}

func newReleaseHasher(algo string, digest []byte) (hash.Hash, error) {
	switch algo {
	case "sha256":
		if len(digest) != sha256.Size {
			return nil, errors.New("invalid SHA-256 digest length")
		}
		return sha256.New(), nil
	case "sha512":
		if len(digest) != sha512.Size {
			return nil, errors.New("invalid SHA-512 digest length")
		}
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
}

func sanitizeCLIError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.URL != "" {
		if parsed, parseErr := url.Parse(urlErr.URL); parseErr == nil && (parsed.RawQuery != "" || parsed.Fragment != "") {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			msg = strings.ReplaceAll(msg, urlErr.URL, parsed.String())
		}
	}
	msg = strings.TrimSpace(msg)
	msg = strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(msg)
	if len(msg) > 240 {
		msg = msg[:240]
	}
	return msg
}
