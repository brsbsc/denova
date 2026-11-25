package versions

import (
	"context"
	"errors"
	"fmt"
	"time"

	workspacechange "denova/internal/workspace/change"
)

func (s *Service) MaybeCreateTimed(settings VersionAutoSettings) (VersionAutoResult, error) {
	return s.maybeCreateTimed(context.Background(), settings)
}

func (s *Service) maybeCreateTimed(ctx context.Context, settings VersionAutoSettings) (VersionAutoResult, error) {
	settings = normalizeVersionAutoSettings(settings)
	if !settings.TimedEnabled {
		return VersionAutoResult{Skipped: true, Reason: "自动版本已关闭"}, nil
	}
	var out VersionAutoResult
	err := workspacechange.WithConsistentWorkspaceSnapshot(ctx, s.workspace, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		_, lastTimedAt, err := s.latestVersionTimes()
		if err != nil {
			return err
		}
		retryAfter := timedVersionRetryAfterTimestamp(lastTimedAt, settings.TimedIntervalMinutes, time.Now())
		if retryAfter > 0 {
			out = VersionAutoResult{Skipped: true, Reason: "未到自动版本最小间隔", RetryAfter: retryAfter}
			return nil
		}
		result, err := s.createLocked(fmt.Sprintf("自动版本：%s", time.Now().Format("2006-01-02 15:04")), VersionSourceTimer, settings)
		if errors.Is(err, ErrVersionClean) {
			out = VersionAutoResult{Skipped: true, Reason: "工作区无变更"}
			return nil
		}
		if err != nil {
			return err
		}
		out = VersionAutoResult{Version: result.Version}
		return nil
	})
	if err != nil {
		return VersionAutoResult{}, err
	}
	return out, nil
}

func normalizeVersionAutoSettings(settings VersionAutoSettings) VersionAutoSettings {
	defaults := DefaultAutoSettings()
	if settings.TimedIntervalMinutes <= 0 {
		settings.TimedIntervalMinutes = defaults.TimedIntervalMinutes
	}
	if settings.Retention <= 0 {
		settings.Retention = defaults.Retention
	}
	return settings
}

func timedVersionRetryAfterTimestamp(createdAt string, intervalMinutes int, now time.Time) time.Duration {
	if createdAt == "" {
		return 0
	}
	if intervalMinutes <= 0 {
		intervalMinutes = DefaultTimedVersionIntervalMinutes
	}
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return 0
	}
	retryAfter := time.Duration(intervalMinutes)*time.Minute - now.Sub(t)
	if retryAfter <= 0 {
		return 0
	}
	return retryAfter
}
