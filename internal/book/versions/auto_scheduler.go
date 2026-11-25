package versions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ScheduleAutoVersion marks the workspace dirty for automatic versioning.
// Repeated changes reset the idle delay; the Git work itself always runs in
// the timer goroutine and therefore never blocks the durable file mutation.
func (s *Service) ScheduleAutoVersion(settings VersionAutoSettings) {
	settings = normalizeVersionAutoSettings(settings)

	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.autoClosed {
		return
	}

	s.autoGeneration++
	s.autoSettings = settings
	if !settings.TimedEnabled {
		s.stopAutoTimerLocked()
		return
	}
	if s.autoRunning {
		return
	}
	s.resetAutoTimerLocked(s.autoVersionIdleDelay, s.autoGeneration)
}

// ConfigureAutoVersion applies a settings change without marking a clean
// workspace dirty. Disabling the feature cancels any pending timer at once.
func (s *Service) ConfigureAutoVersion(settings VersionAutoSettings) {
	settings = normalizeVersionAutoSettings(settings)

	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	if s.autoClosed {
		return
	}
	s.autoSettings = settings
	if !settings.TimedEnabled {
		s.autoGeneration++
		s.stopAutoTimerLocked()
		return
	}
	if s.autoTimer != nil || s.autoRunning {
		s.autoGeneration++
		if !s.autoRunning {
			s.resetAutoTimerLocked(s.autoVersionIdleDelay, s.autoGeneration)
		}
	}
}

// Close stops pending automatic version work for this workspace and waits for
// any in-flight background snapshot to finish before returning.
func (s *Service) Close() {
	s.autoMu.Lock()
	s.autoClosed = true
	s.autoGeneration++
	s.stopAutoTimerLocked()
	cancel := s.autoRunCancel
	done := s.autoDone
	s.autoMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (s *Service) resetAutoTimerLocked(delay time.Duration, generation uint64) {
	s.stopAutoTimerLocked()
	if delay <= 0 {
		delay = defaultAutoVersionIdleDelay
	}
	s.autoTimer = time.AfterFunc(delay, func() {
		s.runScheduledAutoVersion(generation)
	})
}

func (s *Service) stopAutoTimerLocked() {
	if s.autoTimer == nil {
		return
	}
	s.autoTimer.Stop()
	s.autoTimer = nil
}

func (s *Service) runScheduledAutoVersion(generation uint64) {
	s.autoMu.Lock()
	if s.autoClosed || s.autoRunning || generation != s.autoGeneration {
		s.autoMu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.autoTimer = nil
	s.autoRunning = true
	s.autoRunCancel = cancel
	s.autoDone = done
	settings := s.autoSettings
	s.autoMu.Unlock()

	var retryAfter time.Duration
	defer func() {
		cancel()
		if recovered := recover(); recovered != nil {
			slog.ErrorContext(context.Background(), fmt.Sprintf("[versions] Automatic version background task panicked workspace=%q panic=%v", s.workspace, recovered))
			retryAfter = 0
		}
		s.finishScheduledAutoVersion(generation, retryAfter, done)
	}()

	result, err := s.maybeCreateTimed(runCtx, settings)
	retryAfter = result.RetryAfter
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.ErrorContext(context.Background(), fmt.Sprintf("[versions] Automatic version creation failed workspace=%q err=%v", s.workspace, err))
		}
	} else if result.Skipped {
		slog.WarnContext(context.Background(), fmt.Sprintf("[versions] Automatic version creation skipped workspace=%q reason=%q", s.workspace, result.Reason))
	} else if result.Version != nil {
		slog.InfoContext(context.Background(), fmt.Sprintf("[versions] Automatic version created workspace=%q id=%s", s.workspace, result.Version.ID))
	}
}

func (s *Service) finishScheduledAutoVersion(generation uint64, retryAfter time.Duration, done chan struct{}) {
	s.autoMu.Lock()
	defer s.autoMu.Unlock()
	s.autoRunning = false
	s.autoRunCancel = nil
	s.autoDone = nil
	close(done)
	if s.autoClosed {
		return
	}
	if generation != s.autoGeneration {
		if s.autoSettings.TimedEnabled {
			s.resetAutoTimerLocked(s.autoVersionIdleDelay, s.autoGeneration)
		}
		return
	}
	if retryAfter > 0 && s.autoSettings.TimedEnabled {
		s.resetAutoTimerLocked(retryAfter, generation)
	}
}
