package interactive

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"denova/internal/agents/conversationjournal"
)

const storyCheckpointScanTransactions = 1024

type storyEventCheckpoint struct {
	SourceBranchID string
	LatestTurnID   string
	Depth          int
	State          map[string]any
	Plan           *BranchPlan
	ParentState    map[string]any
	ParentPlan     *BranchPlan
}

func (s *Store) stateAndPlanBeforeTurnLocked(storyID, branchID, turnID, parentID string) (map[string]any, *BranchPlan, int, bool, error) {
	handle, err := s.openStoryJournalLocked(storyID, true)
	if err != nil {
		return nil, nil, 0, false, err
	}
	if branchID == "" {
		branchID = handle.projection.Meta.CurrentBranch
	}
	projection := handle.projection.Branches[branchID]
	if projection == nil {
		return nil, nil, 0, false, fmt.Errorf("分支不存在: %s", branchID)
	}
	if projection.LatestTurnID == turnID && projection.StateBeforeLatest != nil && projection.LatestTurnParentID == parentID {
		depth := 0
		if projection.Depth > 0 {
			depth = projection.Depth - 1
		}
		return cloneStoryState(projection.StateBeforeLatest), cloneBranchPlan(projection.PlanBeforeLatest), depth, false, nil
	}
	checkpoint, err := s.checkpointAtStoryEventLocked(handle, parentID)
	if err != nil {
		return nil, nil, 0, false, err
	}
	return cloneStoryState(checkpoint.State), cloneBranchPlan(checkpoint.Plan), checkpoint.Depth, true, nil
}

// checkpointAtStoryEventLocked resolves an exact parent, including state and
// plan edits between turns. The indexed links keep historical turn bodies out
// of memory; the journal supplies the actual state and plan changes.
func (s *Store) checkpointAtStoryEventLocked(handle *storyJournalHandle, eventID string) (storyEventCheckpoint, error) {
	if eventID == "" {
		return storyEventCheckpoint{State: initialStoryState()}, nil
	}
	checkpoint := storyEventCheckpoint{}
	ancestorIDs := make(map[string]bool)
	for id := eventID; id != ""; {
		node, found := handle.projection.ModelNodes[id]
		if !found || ancestorIDs[id] {
			return storyEventCheckpoint{}, fmt.Errorf("story checkpoint ancestry is incomplete at %s", id)
		}
		ancestorIDs[id] = true
		if node.TurnAt != "" {
			checkpoint.Depth++
			if checkpoint.LatestTurnID == "" {
				checkpoint.LatestTurnID = id
			}
		}
		id = node.Parent
	}
	var err error
	checkpoint.State, err = s.stateForStoryAncestorsLocked(handle, ancestorIDs)
	if err != nil {
		return storyEventCheckpoint{}, err
	}
	checkpoint.Plan, err = s.planForStoryAncestorsLocked(handle, ancestorIDs)
	if err != nil {
		return storyEventCheckpoint{}, err
	}
	return checkpoint, nil
}

// checkpointAtTurnLocked resolves one historical turn without materializing
// the story. The backwards pass retains only ancestor IDs; two forward
// streaming passes then account for append-only state revisions and build the
// exact branch checkpoint in chronological order.
func (s *Store) checkpointAtTurnLocked(storyID, turnID string) (storyEventCheckpoint, error) {
	var err error
	storyID, err = normalizeStoryID(storyID)
	if err != nil {
		return storyEventCheckpoint{}, err
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return storyEventCheckpoint{}, fmt.Errorf("父回合不能为空 / Parent turn is required")
	}
	handle, err := s.refreshStoryJournalLocked(storyID, true, true)
	if err != nil {
		return storyEventCheckpoint{}, err
	}
	targetNode, hasTarget := handle.projection.ModelNodes[turnID]
	if !hasTarget || targetNode.TurnAt == "" {
		return storyEventCheckpoint{}, fmt.Errorf("父回合不存在 / Parent turn not found: %s", turnID)
	}
	branchIDs := make([]string, 0, len(handle.projection.Meta.Branches))
	for branchID := range handle.projection.Meta.Branches {
		branchIDs = append(branchIDs, branchID)
	}
	sort.Strings(branchIDs)

	for _, pathBranchID := range branchIDs {
		branchMeta := handle.projection.Meta.Branches[pathBranchID]
		headID := strings.TrimSpace(branchMeta.Head)
		if branchProj := handle.projection.Branches[pathBranchID]; branchProj != nil && strings.TrimSpace(branchProj.Head) != "" {
			headID = strings.TrimSpace(branchProj.Head)
		}
		ancestorIDs := make(map[string]bool)
		visited := make(map[string]bool)
		found := false
		depth := 0
		for id := headID; id != ""; {
			node, ok := handle.projection.ModelNodes[id]
			if !ok || visited[id] {
				break
			}
			visited[id] = true
			if id == turnID {
				found = true
			}
			if found {
				ancestorIDs[id] = true
				if node.TurnAt != "" {
					depth++
				}
			}
			id = node.Parent
		}
		if !found {
			continue
		}
		sourceBranchID, branchErr := s.turnEnvelopeBranchIDLocked(handle, turnID, targetNode.Cursor)
		if branchErr != nil {
			return storyEventCheckpoint{}, branchErr
		}
		state, stateErr := s.stateForStoryAncestorsLocked(handle, ancestorIDs)
		if stateErr != nil {
			return storyEventCheckpoint{}, stateErr
		}
		plan, planErr := s.planForStoryAncestorsLocked(handle, ancestorIDs)
		if planErr != nil {
			return storyEventCheckpoint{}, planErr
		}
		// A creator can revise the plan after the latest Turn without adding a
		// new Turn. Forking that current Turn should inherit the live revision;
		// forks from older Turns must keep their historical checkpoint instead.
		if branch := handle.projection.Branches[pathBranchID]; branch != nil && branch.LatestTurnID == turnID {
			plan = cloneBranchPlan(branch.Plan)
		}
		parentCheckpoint, parentErr := s.checkpointAtStoryEventLocked(handle, targetNode.Parent)
		if parentErr != nil {
			return storyEventCheckpoint{}, parentErr
		}
		return storyEventCheckpoint{
			SourceBranchID: sourceBranchID,
			LatestTurnID:   turnID,
			Depth:          depth,
			State:          state,
			Plan:           plan,
			ParentState:    parentCheckpoint.State,
			ParentPlan:     parentCheckpoint.Plan,
		}, nil
	}
	return storyEventCheckpoint{}, fmt.Errorf("父回合不存在 / Parent turn not found: %s", turnID)
}

func (s *Store) turnEnvelopeBranchIDLocked(handle *storyJournalHandle, turnID string, cursor conversationjournal.Cursor) (string, error) {
	if cursor == 0 {
		return "", fmt.Errorf("missing journal locator for turn %s", turnID)
	}
	records, err := handle.journal.ReadRange(context.Background(), conversationjournal.Range{After: cursor - 1, Through: cursor})
	if err != nil {
		return "", err
	}
	for _, physical := range records {
		_, events, decodeErr := decodeStoryProjectionPayload(physical.Payload)
		if decodeErr != nil {
			return "", decodeErr
		}
		for _, event := range events {
			if event.Envelope.Type == StoryEventTypeTurn && event.Envelope.ID == turnID {
				return event.Envelope.BranchID, nil
			}
		}
	}
	return "", fmt.Errorf("父回合不存在 / Parent turn not found: %s", turnID)
}

func (s *Store) planForStoryAncestorsLocked(handle *storyJournalHandle, ancestorIDs map[string]bool) (*BranchPlan, error) {
	var plan *BranchPlan
	if err := scanStoryEventsLocked(handle, func(record StoryEventRecord) error {
		if record.Envelope.Type != StoryEventTypeBranchPlanUpdated && record.Envelope.Type != StoryEventTypeBranchPlanRevised {
			return nil
		}
		var event BranchPlanUpdatedEvent
		if err := mapToStruct(record.Raw, &event); err != nil {
			return err
		}
		belongsToPath := ancestorIDs[event.TurnID]
		if record.Envelope.Type == StoryEventTypeBranchPlanRevised {
			belongsToPath = ancestorIDs[event.ID]
		}
		if belongsToPath {
			plan = &BranchPlan{Markdown: event.Markdown, UpdatedTurnID: event.TurnID, UpdatedAt: event.Ts, Revision: event.ID}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Store) stateForStoryAncestorsLocked(handle *storyJournalHandle, ancestorIDs map[string]bool) (map[string]any, error) {
	return s.stateForStoryAncestorsContextLocked(context.Background(), handle, ancestorIDs)
}

// The context variant serves cancellable read-only projections without changing
// the checkpoint reducer used by existing branch creation and recovery paths.
func (s *Store) stateForStoryAncestorsContextLocked(ctx context.Context, handle *storyJournalHandle, ancestorIDs map[string]bool) (map[string]any, error) {
	revisions := make(map[string]TurnStateRevisedEvent)
	if err := scanStoryEventsContextLocked(ctx, handle, func(record StoryEventRecord) error {
		if record.Envelope.Type != StoryEventTypeTurnStateRevised {
			return nil
		}
		var revision TurnStateRevisedEvent
		if err := mapToStruct(record.Raw, &revision); err != nil {
			return err
		}
		if ancestorIDs[revision.TurnID] {
			revisions[revision.TurnID] = revision
		}
		return nil
	}); err != nil {
		return nil, err
	}

	state := initialStoryState()
	if err := scanStoryEventsContextLocked(ctx, handle, func(record StoryEventRecord) error {
		if !ancestorIDs[record.Envelope.ID] {
			return nil
		}
		switch record.Envelope.Type {
		case StoryEventTypeStateDelta:
			var delta StateDeltaEvent
			if err := mapToStruct(record.Raw, &delta); err != nil {
				return err
			}
			applyStateDeltaToProjection(state, StateDelta{SchemaVersion: delta.SchemaVersion, Ops: delta.Ops, ActorOps: delta.ActorOps})
		case StoryEventTypeTurn:
			var turn TurnEvent
			// State reduction needs no narrative, thinking or model context.
			if err := mapToStruct(map[string]any{"id": record.Envelope.ID, "state_delta": record.Raw["state_delta"]}, &turn); err != nil {
				return err
			}
			if revision, ok := revisions[turn.ID]; ok {
				if revision.ClearStateDelta {
					turn.StateDelta = nil
				} else if revision.StateDelta != nil {
					value := *revision.StateDelta
					turn.StateDelta = &value
				}
			}
			applyTurnState(state, turn)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return state, nil
}

func scanStoryEventsLocked(handle *storyJournalHandle, visit func(StoryEventRecord) error) error {
	return scanStoryEventsContextLocked(context.Background(), handle, visit)
}

func scanStoryEventsContextLocked(ctx context.Context, handle *storyJournalHandle, visit func(StoryEventRecord) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if handle == nil || handle.journal == nil {
		return fmt.Errorf("story journal is unavailable")
	}
	head := handle.journal.Head().Cursor
	for after := conversationjournal.Cursor(0); after < head; {
		records, err := handle.journal.ReadRange(ctx, conversationjournal.Range{
			After: after, Through: head, Limit: storyCheckpointScanTransactions,
		})
		if err != nil {
			return err
		}
		next := after
		for _, physical := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			if physical.Location.Cursor > next {
				next = physical.Location.Cursor
			}
			_, events, decodeErr := decodeStoryProjectionPayload(physical.Payload)
			if decodeErr != nil {
				return decodeErr
			}
			for _, record := range events {
				if err := visit(record); err != nil {
					return err
				}
			}
		}
		if next <= after {
			return fmt.Errorf("story journal scan made no progress after cursor %d", after)
		}
		after = next
	}
	return nil
}
