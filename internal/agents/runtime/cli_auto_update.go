package agentruntime

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"denova/config"
)

const (
	defaultAutoUpdateDelay     = 30 * time.Second
	defaultAutoUpdateTick      = 5 * time.Minute
	defaultAutoUpdateInterval  = 24 * time.Hour
	defaultAutoUpdateRetryWait = time.Hour
)

// RunAutoUpdate periodically checks and updates only already-installed managed
// CLIs when enabled() returns true. Callers own goroutine lifecycle and ctx
// cancellation; Close() also terminates this loop.
func (m *CLIManager) RunAutoUpdate(ctx context.Context, enabled func() (bool, error)) {
	if !m.Enabled() {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	stopCancel := context.AfterFunc(m.baseCtx, cancel)
	defer stopCancel()
	defer cancel()

	timer := time.NewTimer(m.autoUpdateDelay)
	defer timer.Stop()

	for {
		select {
		case <-runCtx.Done():
			return
		case <-timer.C:
			m.runAutoUpdateCycle(runCtx, enabled)
			timer.Reset(m.autoUpdateTick)
		}
	}
}

func (m *CLIManager) runAutoUpdateCycle(ctx context.Context, enabled func() (bool, error)) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "[agent-runtime] panic during managed CLI auto-update cycle", "panic", r)
		}
	}()
	if enabled == nil {
		return
	}
	allow, err := enabled()
	if err != nil {
		slog.WarnContext(ctx, "[agent-runtime] failed to read CLI auto-update setting", "error", err)
		return
	}
	if !allow {
		return
	}
	for _, id := range managedCLIEngines {
		if ctx.Err() != nil {
			return
		}
		m.autoUpdateEngine(ctx, id)
	}
}

func (m *CLIManager) autoUpdateEngine(ctx context.Context, id config.RuntimeID) {
	now := m.now().UTC()

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	entry := m.entries[id]
	if entry == nil || !entry.status.Installed || entry.busy {
		m.mu.Unlock()
		return
	}
	needCheck := entry.lastChecked.IsZero() || now.Sub(entry.lastChecked) >= m.autoUpdateInterval
	checkBackedOff := !entry.lastCheckFailedAt.IsZero() && now.Sub(entry.lastCheckFailedAt) < m.autoUpdateRetryWait
	m.mu.Unlock()

	if needCheck && !checkBackedOff {
		if _, err := m.CheckUpdate(ctx, id); err != nil {
			if !errors.Is(err, ErrCLIOperationBusy) && ctx.Err() == nil {
				slog.WarnContext(ctx, "[agent-runtime] automatic CLI update check failed", "runtime", id, "error", err)
			}
			return
		}
	}

	now = m.now().UTC()
	m.mu.Lock()
	if m.closed || entry.busy || !entry.status.Installed || !entry.status.UpdateAvailable {
		m.mu.Unlock()
		return
	}
	targetVersion := entry.status.LatestVersion
	if targetVersion == "" {
		m.mu.Unlock()
		return
	}
	// Do not loop on a release that already failed automatic installation until
	// upstream publishes a newer version or the user triggers a manual update.
	if entry.failedAutoVersion != "" && sameSemver(entry.failedAutoVersion, targetVersion) {
		m.mu.Unlock()
		return
	}
	if !entry.lastInstallAttempt.IsZero() && now.Sub(entry.lastInstallAttempt) < m.autoUpdateRetryWait {
		m.mu.Unlock()
		return
	}
	entry.busy = true
	entry.status.State = "updating"
	entry.status.LastError = ""
	entry.status.BytesDownloaded = 0
	entry.status.TotalBytes = 0
	entry.bytesDownloaded.Store(0)
	entry.lastInstallAttempt = now
	m.mu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "[agent-runtime] panic during automatic CLI update", "runtime", id, "panic", r)
			m.recordInstallFailure(id, true, targetVersion, errors.New("unexpected panic during automatic CLI update"))
		}
	}()
	m.runInstall(ctx, id, true, targetVersion)
}
