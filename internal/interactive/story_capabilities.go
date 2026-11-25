package interactive

import (
	"context"
	"encoding/json"
	"errors"

	"denova/internal/agents/conversationjournal"
	"denova/internal/agents/sessionjournal"
	agentsession "github.com/alfredxw/denova/agent/session"
)

// LoadCapability reads a branch's runtime state from the Story journal.
func (s *Store) LoadCapability(ctx context.Context, storyID string, key agentsession.Key, capability string) (json.RawMessage, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	var err error
	storyID, err = normalizeStoryID(storyID)
	if err != nil {
		return nil, false, err
	}
	release, err := s.acquireStoryReadLeaseLocked(storyID)
	if err != nil {
		return nil, false, err
	}
	defer release()
	handle, err := s.refreshStoryJournalLocked(storyID, false, false)
	if err != nil {
		return nil, false, err
	}
	return handle.projection.AgentSessions.Capability(key, capability)
}

// UpdateCapabilities atomically serializes related peer-runtime state with Story mutations.
// The selected executor owns the capability; this creates no Native Agent.
func (s *Store) UpdateCapabilities(ctx context.Context, storyID string, key agentsession.Key, upgrade string, update func(sessionjournal.CapabilityReader) (map[string]json.RawMessage, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	storyID, err = normalizeStoryID(storyID)
	if err != nil {
		return err
	}
	release, err := s.acquireStoryMutationLeaseLocked(storyID)
	if err != nil {
		return err
	}
	defer release()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		handle, err := s.refreshStoryJournalLocked(storyID, true, true)
		if err != nil {
			return err
		}
		records, err := handle.projection.AgentSessions.CapabilityChanges(key, update)
		if err != nil || len(records) == 0 {
			return err
		}
		payloads := make([]json.RawMessage, len(records))
		for i, record := range records {
			payloads[i], err = json.Marshal(record)
			if err != nil {
				return err
			}
		}
		head := handle.journal.Head()
		_, err = handle.journal.AppendWithBackup(ctx, conversationjournal.Guard{Cursor: head.Cursor, RecordSHA256: head.RecordSHA256}, upgrade, payloads...)
		if err == nil {
			return nil
		}
		if errors.Is(err, conversationjournal.ErrConflict) {
			continue
		}
		if errors.Is(err, conversationjournal.ErrCommitUnknown) {
			_ = s.evictStoryJournalLocked(storyID)
		}
		return err
	}
}
