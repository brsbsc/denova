package interactive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"denova/internal/agents/conversationjournal"
	"denova/internal/agents/sessionjournal"
	interactivestate "denova/internal/interactive/state"

	agentsession "github.com/alfredxw/denova/agent/session"
)

func TestStoryIDValidationBlocksPathTraversalAndNormalizesWhitespace(t *testing.T) {
	root := t.TempDir()
	novaDir := t.TempDir()
	store := NewStoreWithNovaDir(root, novaDir)
	defer store.Close()

	story, err := store.CreateStory(CreateStoryRequest{Title: "Traversal guard"})
	if err != nil {
		t.Fatal(err)
	}

	outsideVictim := filepath.Join(root, "interactive", "story-outside.jsonl")
	if err := os.WriteFile(outsideVictim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	invalidIDs := []string{
		"",
		"   ",
		".",
		"..",
		"../outside",
		`..\outside`,
		"nested/story",
		`nested\story`,
		"story..bad",
		"story\x00bad",
		"story with space",
		"CON",
	}
	key := agentsession.Key{Namespace: "game", ID: "main"}
	for _, badID := range invalidIDs {
		if _, err := store.Snapshot(badID, "main"); err == nil {
			t.Fatalf("Snapshot(%q) unexpectedly succeeded", badID)
		}
		if err := store.DeleteStory(badID); err == nil {
			t.Fatalf("DeleteStory(%q) unexpectedly succeeded", badID)
		}
		if err := store.SelectStory(badID); err == nil {
			t.Fatalf("SelectStory(%q) unexpectedly succeeded", badID)
		}
		if _, err := store.AppendTurn(badID, AppendTurnRequest{BranchID: "main", User: "u", Narrative: "n"}); err == nil {
			t.Fatalf("AppendTurn(%q) unexpectedly succeeded", badID)
		}
		if err := store.AppendTokenUsageEvent(badID, TokenUsageEvent{BranchID: "main", TotalTokens: 10}); err == nil {
			t.Fatalf("AppendTokenUsageEvent(%q) unexpectedly succeeded", badID)
		}
		if _, err := store.ReadSceneAtTurn(context.Background(), badID, "main", "ev-1", "ev-1"); err == nil {
			t.Fatalf("ReadSceneAtTurn(%q) unexpectedly succeeded", badID)
		}
		if _, err := OpenAgentLog(context.Background(), root, novaDir, badID, key); err == nil {
			t.Fatalf("OpenAgentLog(%q) unexpectedly succeeded", badID)
		}
		if _, err := AgentSessionKeys(root, novaDir, badID); err == nil {
			t.Fatalf("AgentSessionKeys(%q) unexpectedly succeeded", badID)
		}
		if _, err := OpenAgentReader(context.Background(), root, novaDir, badID, key); err == nil {
			t.Fatalf("OpenAgentReader(%q) unexpectedly succeeded", badID)
		}
		if _, err := NewStoryToolArtifactStore(root, badID, "main"); err == nil {
			t.Fatalf("NewStoryToolArtifactStore(%q) unexpectedly succeeded", badID)
		}
	}
	if data, err := os.ReadFile(outsideVictim); err != nil || string(data) != "keep" {
		t.Fatalf("outside file was modified or deleted: err=%v data=%q", err, data)
	}

	identity := DomainCommitIdentity{CommandID: "cmd-space", OperationID: "op-space", Cycle: 1}
	inputIntent, err := NewPlayerInputIntent(identity, "main", "Look around")
	if err != nil {
		t.Fatal(err)
	}
	paddedID := "  " + story.ID + "  "
	if _, err := store.CommitPlayerInput(paddedID, inputIntent); err != nil {
		t.Fatalf("CommitPlayerInput with padded storyID failed: %v", err)
	}
	if err := store.SaveTurnDraft(paddedID, "main", TurnDraft{
		Identity:        identity,
		Narrative:       "Draft prose",
		NarrativeSource: "stream",
	}, nil); err != nil {
		t.Fatalf("SaveTurnDraft with padded storyID failed: %v", err)
	}
	draft, found, err := store.LoadTurnDraft(paddedID, "main", identity)
	if err != nil || !found || draft.Narrative != "Draft prose" {
		t.Fatalf("LoadTurnDraft with padded storyID = (%+v, %t, %v)", draft, found, err)
	}
}

func TestRewindAndBranchPreserveParentStateAndPlanAcrossColdReplay(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()

	story, err := store.CreateStory(CreateStoryRequest{
		Title:        "Rewind and branch checkpoints",
		PlanningMode: StoryPlanningModeEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}

	plan1 := "## Act 1\n\nSecure the lighthouse."
	turn1, _, err := store.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID:  "main",
		User:      "Enter lighthouse",
		Narrative: "You step into the lighthouse.",
		Ops: []interactivestate.Op{
			{Op: "set", Path: "characters.Lin.hp", Value: 75},
			{Op: "set", Path: "world.stage", Value: "foyer"},
		},
		TurnResult: &TurnResult{Choices: testTurnChoices(), PlanUpdate: &plan1},
	})
	if err != nil {
		t.Fatal(err)
	}

	checkReq := sampleTurnCheckRequest()
	stairsResolution, err := resolveTurnRulesWithSeed(story.ID, "main", initialStoryState(), checkReq, 7)
	if err != nil {
		t.Fatal(err)
	}
	plan2 := "## Act 2\n\nClimb to the lantern room."
	turn2, _, err := store.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID:       "main",
		User:           "Climb stairs",
		Narrative:      "You ascend the spiral stairs.",
		Ops:            []interactivestate.Op{{Op: "set", Path: "world.stage", Value: "stairs"}},
		RuleResolution: &stairsResolution,
		TurnResult:     &TurnResult{Choices: testTurnChoices(), PlanUpdate: &plan2},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan3 := "## Act 3\n\nLight the beacon."
	turn3, _, err := store.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID:   "main",
		User:       "Open hatch",
		Narrative:  "You reach the lantern hatch.",
		Ops:        []interactivestate.Op{{Op: "set", Path: "world.stage", Value: "hatch"}},
		TurnResult: &TurnResult{Choices: testTurnChoices(), PlanUpdate: &plan3},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RewindToTurnParent(story.ID, RewindTurnRequest{BranchID: "main", TurnID: turn3.ID}); err != nil {
		t.Fatal(err)
	}

	reopenCold := func() {
		t.Helper()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(conversationjournal.SidecarPath(store.storyPath(story.ID))); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	reopenCold()
	ctxAtTurn2Parent, err := store.StoryContextAtTurnParent(story.ID, "main", turn2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotHP, gotStage := getPath(ctxAtTurn2Parent.Snapshot.State, "characters.Lin.hp"), getPath(ctxAtTurn2Parent.Snapshot.State, "world.stage"); gotHP != 75.0 || gotStage != "foyer" {
		t.Fatalf("after rewind StoryContextAtTurnParent state = hp:%v stage:%v, want 75/foyer", gotHP, gotStage)
	}
	if ctxAtTurn2Parent.Snapshot.BranchPlan == nil || ctxAtTurn2Parent.Snapshot.BranchPlan.Markdown != plan1 {
		t.Fatalf("after rewind StoryContextAtTurnParent plan = %#v, want %q", ctxAtTurn2Parent.Snapshot.BranchPlan, plan1)
	}

	if _, err := store.AppendStateDelta(story.ID, AppendStateDeltaRequest{
		BranchID: "main",
		ParentID: turn2.ID,
		Ops:      []interactivestate.Op{{Op: "set", Path: "world.weather", Value: "storm"}},
	}); err != nil {
		t.Fatal(err)
	}
	reopenCold()
	snapAfterDelta, err := store.Snapshot(story.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if getPath(snapAfterDelta.State, "characters.Lin.hp") != 75.0 || getPath(snapAfterDelta.State, "world.weather") != "storm" {
		t.Fatalf("state after AppendStateDelta post-rewind collapsed: %#v", snapAfterDelta.State)
	}

	if _, err := store.RerollRuleResolution(story.ID, stairsResolution.ID, RuleResolutionRerollRequest{BranchID: "main", TurnID: turn2.ID}); err != nil {
		t.Fatal(err)
	}

	replacedTurn2, _, err := store.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID:      "main",
		ReplaceTurnID: turn2.ID,
		User:          "Climb stairs",
		Narrative:     "You take the hidden service lift instead.",
		Ops:           []interactivestate.Op{{Op: "set", Path: "world.stage", Value: "lift"}},
		TurnResult:    &TurnResult{Choices: testTurnChoices()},
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenCold()
	snapAfterReplace, err := store.Snapshot(story.ID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if gotHP, gotStage := getPath(snapAfterReplace.State, "characters.Lin.hp"), getPath(snapAfterReplace.State, "world.stage"); gotHP != 75.0 || gotStage != "lift" {
		t.Fatalf("state after regenerate post-rewind = hp:%v stage:%v, want 75/lift", gotHP, gotStage)
	}

	fork, err := store.CreateBranch(story.ID, CreateBranchRequest{
		ParentEventID: replacedTurn2.ID,
		Title:         "Fork at lift",
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenCold()
	forkParentCtx, err := store.StoryContextAtTurnParent(story.ID, fork.ID, replacedTurn2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if forkParentCtx.Snapshot.CurrentTurn == nil || forkParentCtx.Snapshot.CurrentTurn.ID != turn1.ID {
		t.Fatalf("fork parent current turn = %#v, want %s", forkParentCtx.Snapshot.CurrentTurn, turn1.ID)
	}
	if gotHP, gotStage := getPath(forkParentCtx.Snapshot.State, "characters.Lin.hp"), getPath(forkParentCtx.Snapshot.State, "world.stage"); gotHP != 75.0 || gotStage != "foyer" {
		t.Fatalf("fork StoryContextAtTurnParent state = hp:%v stage:%v, want 75/foyer", gotHP, gotStage)
	}
	if forkParentCtx.Snapshot.BranchPlan == nil || forkParentCtx.Snapshot.BranchPlan.Markdown != plan1 {
		t.Fatalf("fork StoryContextAtTurnParent plan = %#v, want %q", forkParentCtx.Snapshot.BranchPlan, plan1)
	}

	forkReplacement, _, err := store.AppendTurnWithState(story.ID, AppendTurnWithStateRequest{
		BranchID:      fork.ID,
		ReplaceTurnID: replacedTurn2.ID,
		User:          "Climb stairs",
		Narrative:     "On the fork, you scale the outer ladder.",
		Ops:           []interactivestate.Op{{Op: "set", Path: "world.stage", Value: "ladder"}},
		TurnResult:    &TurnResult{Choices: testTurnChoices()},
	})
	if err != nil {
		t.Fatal(err)
	}
	reopenCold()
	forkSnap, err := store.Snapshot(story.ID, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if forkSnap.CurrentTurn == nil || forkSnap.CurrentTurn.ID != forkReplacement.ID {
		t.Fatalf("fork head = %#v, want %s", forkSnap.CurrentTurn, forkReplacement.ID)
	}
	if gotHP, gotStage := getPath(forkSnap.State, "characters.Lin.hp"), getPath(forkSnap.State, "world.stage"); gotHP != 75.0 || gotStage != "ladder" {
		t.Fatalf("fork state after cold replay = hp:%v stage:%v, want 75/ladder", gotHP, gotStage)
	}
}

func TestReadOnlyQueriesDoNotMigrateCanonicalStoryJSONL(t *testing.T) {
	root := t.TempDir()
	seed := NewStore(root)
	story, err := seed.CreateStory(CreateStoryRequest{Title: "Read-only v0.3.3"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := seed.AppendTurn(story.ID, AppendTurnRequest{BranchID: "main", User: "hello", Narrative: "world"})
	if err != nil {
		t.Fatal(err)
	}
	meta, events, err := seed.readStoryJournalLocked(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	meta.StoryDirectorID = DefaultStoryDirectorID
	compaction := map[string]any{
		"v": 1, "type": "context_compaction", "id": "cc-legacy",
		"parent_id": turn.ID, "branch_id": "main", "ts": turn.Ts,
		"epoch": 1, "summary": "Legacy summary",
	}
	// Write a legacy preset story (valid for read-only projection without compaction rows).
	if err := writeJSONL(seed.storyPath(story.ID), []any{meta, events[0].Raw}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(conversationjournal.SidecarPath(seed.storyPath(story.ID))); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	originalPresetBytes, err := os.ReadFile(seed.storyPath(story.ID))
	if err != nil {
		t.Fatal(err)
	}

	reader := NewStore(root)
	if _, _, err := reader.FindDomainTurnCommit(story.ID, "main", DomainCommitIdentity{CommandID: "c1", OperationID: "o1", Cycle: 1}, "sha256:00"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.FindRecentDomainTurnCommit(story.ID, "main", DomainCommitIdentity{CommandID: "c1", OperationID: "o1", Cycle: 1}, "sha256:00"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reader.LoadCapability(context.Background(), story.ID, agentsession.Key{Namespace: "game", ID: "main"}, "cap"); err != nil {
		t.Fatal(err)
	}
	if _, err := AgentSessionKeys(root, "", story.ID); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	afterReadOnlyBytes, err := os.ReadFile(seed.storyPath(story.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterReadOnlyBytes, originalPresetBytes) {
		t.Fatalf("read-only operations mutated canonical story JSONL:\nbefore=%s\nafter=%s", originalPresetBytes, afterReadOnlyBytes)
	}

	// Now verify compaction migration with empty novaDir backs up inside storyDir/migrations instead of root/.denova.
	branch := meta.Branches["main"]
	branch.Head = "cc-legacy"
	meta.Branches["main"] = branch
	meta.StoryDirectorID = ""
	if err := writeJSONL(seed.storyPath(story.ID), []any{meta, events[0].Raw, compaction}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(conversationjournal.SidecarPath(seed.storyPath(story.ID))); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := writeAtomicBytes(seed.indexPath(), []byte(`{"stories":[]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	coldReader := NewStore(root)
	// openStoryJournalLocked(false) does not rewrite the file even when index version is 0.
	_, _, _ = coldReader.FindDomainTurnCommit(story.ID, "main", DomainCommitIdentity{CommandID: "c1", OperationID: "o1", Cycle: 1}, "sha256:00")
	_ = coldReader.Close()
	stillUnmigrated, err := os.ReadFile(seed.storyPath(story.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stillUnmigrated, []byte("context_compaction")) {
		t.Fatal("read-only FindDomainTurnCommit unexpectedly ran compaction migration")
	}

	migrator := NewStore(root)
	defer migrator.Close()
	if _, err := migrator.StoryContext(story.ID, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".denova")); !os.IsNotExist(err) {
		t.Fatalf("migration created forbidden content-root .denova directory: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(seed.storyDir(), "migrations", legacyStoryCompactionMigrationName, "*", filepath.Base(seed.storyPath(story.ID))))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected backup under storyDir/migrations, got %v (err=%v)", backups, err)
	}
}

func TestDeleteStoryPreservesCanonicalFilesWhenIndexWriteFails(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	defer store.Close()

	story, err := store.CreateStory(CreateStoryRequest{Title: "Safe delete"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendTurn(story.ID, AppendTurnRequest{BranchID: "main", User: "act", Narrative: "scene"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendTokenUsageEvent(story.ID, TokenUsageEvent{BranchID: "main", TotalTokens: 42}); err != nil {
		t.Fatal(err)
	}

	injectedErr := errors.New("simulated index write failure")
	store.writeIndex = func(Index) error { return injectedErr }
	if err := store.DeleteStory(story.ID); !errors.Is(err, injectedErr) {
		t.Fatalf("DeleteStory error = %v, want %v", err, injectedErr)
	}
	if _, err := os.Stat(store.storyPath(story.ID)); err != nil {
		t.Fatalf("canonical story file was deleted before index commit: %v", err)
	}
	if _, err := os.Stat(store.usagePath(story.ID)); err != nil {
		t.Fatalf("usage file was deleted before index commit: %v", err)
	}

	store.writeIndex = nil
	if err := store.DeleteStory(story.ID); err != nil {
		t.Fatalf("DeleteStory retry failed: %v", err)
	}
	if _, err := os.Stat(store.storyPath(story.ID)); !os.IsNotExist(err) {
		t.Fatalf("canonical story file still exists after committed delete: %v", err)
	}
	idx, err := store.Index()
	if err != nil || len(idx.Stories) != 0 {
		t.Fatalf("index after delete = %+v, err=%v", idx, err)
	}
}

func TestUpdateCapabilitiesRetriesOnConcurrentAgentAppendConflict(t *testing.T) {
	root := t.TempDir()
	novaDir := t.TempDir()
	store := NewStoreWithNovaDir(root, novaDir)
	defer store.Close()

	story, err := store.CreateStory(CreateStoryRequest{Title: "Capability CAS retry"})
	if err != nil {
		t.Fatal(err)
	}
	key := agentsession.Key{Namespace: "game", ID: "main"}
	meta, err := readStoryJournalHeader(store.storyPath(story.ID))
	if err != nil {
		t.Fatal(err)
	}
	peerProjection := newStoryJournalProjection(story.ID, storyJournalGeneration(meta))
	peerJournal, err := conversationjournal.Open(
		context.Background(),
		store.storyPath(story.ID),
		conversationjournal.Identity{ID: story.ID, Generation: storyJournalGeneration(meta)},
		peerProjection,
		conversationjournal.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer peerJournal.Close()

	attempts := 0
	err = store.UpdateCapabilities(context.Background(), story.ID, key, "", func(sessionjournal.CapabilityReader) (map[string]json.RawMessage, error) {
		attempts++
		if attempts == 1 {
			// Advance the underlying journal file through an independent journal
			// handle so the first AppendWithBackup hits ErrConflict.
			peerRecords, changeErr := peerProjection.AgentSessions.CapabilityChanges(key, func(sessionjournal.CapabilityReader) (map[string]json.RawMessage, error) {
				return map[string]json.RawMessage{"peer.other": json.RawMessage(`{"step":1}`)}, nil
			})
			if changeErr != nil {
				return nil, changeErr
			}
			rawRecord, marshalErr := json.Marshal(peerRecords[0])
			if marshalErr != nil {
				return nil, marshalErr
			}
			peerHead := peerJournal.Head()
			if _, appendErr := peerJournal.AppendWithBackup(
				context.Background(),
				conversationjournal.Guard{Cursor: peerHead.Cursor, RecordSHA256: peerHead.RecordSHA256},
				"",
				rawRecord,
			); appendErr != nil {
				return nil, appendErr
			}
		}
		return map[string]json.RawMessage{"peer.state": json.RawMessage(`{"ready":true}`)}, nil
	})
	if err != nil {
		t.Fatalf("UpdateCapabilities failed instead of retrying ErrConflict: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("attempts = %d, expected at least 2 (CAS retry)", attempts)
	}
	raw, ok, err := store.LoadCapability(context.Background(), story.ID, key, "peer.state")
	if err != nil || !ok || string(raw) != `{"ready":true}` {
		t.Fatalf("LoadCapability = (%s, %t, %v)", raw, ok, err)
	}
}
