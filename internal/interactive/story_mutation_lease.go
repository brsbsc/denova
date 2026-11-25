package interactive

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"denova/internal/localfs"
)

// acquireStoryMutationLeaseLocked linearizes the read/CAS/write section across
// every Store instance (and process) that targets the same canonical story.
// Public Store APIs do not expose a context, so acquisition intentionally waits
// without an elapsed-time timeout; process shutdown/crash still releases the
// underlying OS lease.
func (s *Store) acquireStoryMutationLeaseLocked(storyID string) (func(), error) {
	var err error
	storyID, err = normalizeStoryID(storyID)
	if err != nil {
		return nil, err
	}
	releaseOS, err := s.acquireStoryOSLeaseLocked(storyID)
	if err != nil {
		return nil, err
	}
	if s.heldStoryWriteLeases == nil {
		s.heldStoryWriteLeases = make(map[string]int)
	}
	s.heldStoryWriteLeases[storyID]++
	return func() {
		s.heldStoryWriteLeases[storyID]--
		if s.heldStoryWriteLeases[storyID] <= 0 {
			delete(s.heldStoryWriteLeases, storyID)
		}
		releaseOS()
	}, nil
}

// acquireStoryReadLeaseLocked protects readers from observing and repairing a
// partial append written by another Store without granting canonical write
// permission inside this Store.
func (s *Store) acquireStoryReadLeaseLocked(storyID string) (func(), error) {
	var err error
	storyID, err = normalizeStoryID(storyID)
	if err != nil {
		return nil, err
	}
	return s.acquireStoryOSLeaseLocked(storyID)
}

func (s *Store) acquireStoryOSLeaseLocked(storyID string) (func(), error) {
	if s.heldStoryLeases != nil && s.heldStoryLeases[storyID] > 0 {
		s.heldStoryLeases[storyID]++
		return func() { s.releaseStoryOSLeaseLocked(storyID) }, nil
	}
	release, err := localfs.AcquireLease(context.Background(), s.storyPath(storyID)+".mutation.lock")
	if err != nil {
		return nil, fmt.Errorf("acquire story mutation lease: %w", err)
	}
	if s.heldStoryLeases == nil {
		s.heldStoryLeases = make(map[string]int)
	}
	if s.storyLeaseReleases == nil {
		s.storyLeaseReleases = make(map[string]func() error)
	}
	s.heldStoryLeases[storyID] = 1
	s.storyLeaseReleases[storyID] = release
	return func() { s.releaseStoryOSLeaseLocked(storyID) }, nil
}

func (s *Store) releaseStoryOSLeaseLocked(storyID string) {
	if s.heldStoryLeases == nil {
		return
	}
	s.heldStoryLeases[storyID]--
	if s.heldStoryLeases[storyID] > 0 {
		return
	}
	delete(s.heldStoryLeases, storyID)
	release := s.storyLeaseReleases[storyID]
	delete(s.storyLeaseReleases, storyID)
	if release != nil {
		if err := release(); err != nil {
			slog.ErrorContext(context.Background(), fmt.Sprintf("[interactive-story] release mutation lease failed story_id=%s error=%v", storyID, err))
		}
	}
}

func (s *Store) acquireIndexLeaseLocked() (func(), error) {
	if s.heldIndexLeases > 0 {
		s.heldIndexLeases++
		return func() { s.releaseIndexLeaseLocked() }, nil
	}
	if err := os.MkdirAll(s.storyDir(), 0o755); err != nil {
		return nil, fmt.Errorf("create story catalog directory: %w", err)
	}
	release, err := localfs.AcquireLease(context.Background(), s.indexPath()+".lock")
	if err != nil {
		return nil, fmt.Errorf("acquire story index lease: %w", err)
	}
	s.heldIndexLeases = 1
	s.indexLeaseRelease = release
	return func() { s.releaseIndexLeaseLocked() }, nil
}

func (s *Store) releaseIndexLeaseLocked() {
	if s.heldIndexLeases <= 0 {
		return
	}
	s.heldIndexLeases--
	if s.heldIndexLeases > 0 {
		return
	}
	release := s.indexLeaseRelease
	s.indexLeaseRelease = nil
	if release != nil {
		if err := release(); err != nil {
			slog.ErrorContext(context.Background(), fmt.Sprintf("[interactive-story] release index lease failed error=%v", err))
		}
	}
}
