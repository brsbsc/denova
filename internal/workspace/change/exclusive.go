package change

import (
	"context"
	"errors"
	"os"
	"strings"

	"denova/internal/concurrency"
	"denova/internal/localfs"
)

type exclusiveWorkspaceMode uint8

const (
	exclusiveWorkspaceMutation exclusiveWorkspaceMode = iota
	exclusiveWorkspaceSnapshot
)

var workspaceExclusiveLocks = concurrency.NewKeyedLock(localfs.CanonicalPath)

// WithConsistentWorkspaceSnapshot serializes a read-only workspace snapshot
// with active or newly opened workspace-change mutations for workspace, without
// creating a workspace-local change ledger when no service is open yet.
func WithConsistentWorkspaceSnapshot(ctx context.Context, workspace string, action func() error) error {
	if action == nil {
		return newError(ErrorCodeConflict, "exclusive workspace action is nil", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return action()
	}
	abs, err := normalizeWorkspace(workspace)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			unlock := workspaceExclusiveLocks.Lock(workspace)
			defer unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return action()
		}
		return err
	}
	unlock := workspaceExclusiveLocks.Lock(abs)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	workspaceServices.Lock()
	service := workspaceServices.items[abs]
	workspaceServices.Unlock()
	if service != nil {
		return service.withLockedWorkspace(ctx, action, exclusiveWorkspaceSnapshot)
	}
	return action()
}

// WithExclusiveWorkspace runs an unmanaged workspace operation under the same
// mutation lock used by editor saves, Agent writes, review, and history. It is
// intended for operations such as a foreground shell command whose individual
// filesystem effects cannot be expressed as a ChangeSet.
func (s *Service) WithExclusiveWorkspace(ctx context.Context, action func() error) error {
	return s.withExclusiveWorkspace(ctx, action, exclusiveWorkspaceMutation)
}

// WithConsistentWorkspaceSnapshot serializes a read-only workspace snapshot
// with visible mutations without invalidating durable editor-save receipts.
func (s *Service) WithConsistentWorkspaceSnapshot(ctx context.Context, action func() error) error {
	return s.withExclusiveWorkspace(ctx, action, exclusiveWorkspaceSnapshot)
}

func (s *Service) withExclusiveWorkspace(ctx context.Context, action func() error, mode exclusiveWorkspaceMode) error {
	if s == nil {
		return newError(ErrorCodeConflict, "change service is nil", nil)
	}
	if action == nil {
		return newError(ErrorCodeConflict, "exclusive workspace action is nil", nil)
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	unlock := workspaceExclusiveLocks.Lock(s.workspace)
	defer unlock()
	return s.withLockedWorkspace(ctx, action, mode)
}

func (s *Service) withLockedWorkspace(ctx context.Context, action func() error, mode exclusiveWorkspaceMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.contextError(ctx); err != nil {
		return err
	}
	if err := s.reconcilePendingDurabilityLocked(); err != nil {
		return err
	}
	err := action()
	if mode == exclusiveWorkspaceMutation {
		// An unmanaged command may have changed any path, so an old idempotent
		// editor-save receipt can no longer be trusted.
		s.pendingSaves = map[string]pendingSaveIntent{}
	}
	return err
}
