package interactiveapp

import (
	"context"
	"testing"

	"denova/config"
	"denova/internal/agents"
	agentchat "denova/internal/agents/chat"
	agentinteractive "denova/internal/agents/interactive"
	"denova/internal/agents/prompts"
	agentrun "denova/internal/agents/run"
	"denova/internal/agents/runtime/external"
	"denova/internal/interactive"
)

func TestCompactExternalPersistsCompactionDisplayEventAcrossBranchPlanRevision(t *testing.T) {
	workspace := t.TempDir()
	store := interactive.NewStore(workspace)
	t.Cleanup(func() { _ = store.Close() })
	story, err := store.CreateStory(interactive.CreateStoryRequest{
		Title:         "External compaction display",
		StoryTellerID: "classic",
		PlanningMode:  interactive.StoryPlanningModeEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	storyID := story.ID
	selection := config.RuntimeSelection{Kind: config.RuntimeCodex}
	cfg := config.Config{
		Workspace:          workspace,
		ProjectID:          "project-test",
		ProjectStoreDir:    store.Root(),
		Language:           "en-US",
		ActiveAgentRuntime: &selection,
	}
	plan := "## Initial Plan\n\nFollow the river."
	turn1, _, err := store.AppendTurnWithState(storyID, interactive.AppendTurnWithStateRequest{
		BranchID:   "main",
		User:       "Start journey",
		Narrative:  "You walk beside the river.",
		TurnResult: &interactive.TurnResult{Choices: []string{"North", "South", "East", "West", "Wait"}, PlanUpdate: &plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	initialSnap, err := store.Snapshot(storyID, "main")
	if err != nil || initialSnap.BranchPlan == nil {
		t.Fatalf("initial snapshot plan missing: %+v %v", initialSnap.BranchPlan, err)
	}
	// Advance branch.Head to a non-Turn BranchPlanRevisedEvent to cover both
	// CompactExternal and creator-revised branch heads.
	if _, err := store.UpdateBranchPlan(storyID, interactive.UpdateBranchPlanRequest{
		BranchID:     "main",
		BaseRevision: initialSnap.BranchPlan.Revision,
		Markdown:     "## Revised Plan\n\nCross the stone bridge.",
	}); err != nil {
		t.Fatal(err)
	}

	adapter := gameAdapterFunc(func(_ context.Context, input external.Input, host external.Host) (external.Result, error) {
		if input.Mode != external.OperationCompact {
			t.Fatalf("unexpected mode %q", input.Mode)
		}
		if err := host.Emit(agentrun.Event{
			Type: "context_compaction",
			Data: map[string]any{"id": "cmp-1", "status": "completed", "summary": "Compacted context."},
		}); err != nil {
			return external.Result{}, err
		}
		return external.Result{}, nil
	})
	rt := &external.Runtime{
		Selection: config.RuntimeSelection{Kind: config.RuntimeCodex},
		Acquire: func(context.Context) (external.Adapter, func(), error) {
			return adapter, func() {}, nil
		},
		CacheRoot: t.TempDir(),
	}
	conversation := NewConversation(store, t.TempDir(), cfg.Workspace, storyID, "main", "", 800, &cfg)
	assembly, err := agents.BuildExternalGameAssembly(
		t.Context(),
		&cfg,
		nil,
		prompts.InteractiveStorySystemInstructionInput{},
		agents.AgentHostCapabilities{Interactive: true},
		agentinteractive.InteractiveStoryToolContext{
			Store:                  store,
			StoryID:                storyID,
			BranchID:               "main",
			PrepareTurn:            conversation.PrepareInteractiveTurn,
			SubmitTurnResult:       conversation.SubmitTurnResult,
			TurnResultReady:        conversation.InteractiveNarrativeReady,
			LoadNarrativeCandidate: conversation.LoadAgentNarrativeCandidate,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := CompactExternal(context.Background(), ExternalTurnConfig{
		Runtime:      rt,
		Conversation: conversation,
		Config:       cfg,
		Assembly:     assembly,
		Request:      agentchat.ChatRequest{CommandID: "compact-cmd-1"},
	})
	if err != nil {
		t.Fatalf("CompactExternal failed: %v", err)
	}
	if !result.Triggered || !result.RuntimeManaged {
		t.Fatalf("unexpected compaction result: %+v", result)
	}

	reloaded, err := store.Snapshot(storyID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentTurn == nil || reloaded.CurrentTurn.ID != turn1.ID {
		t.Fatalf("CurrentTurn = %+v, want %s", reloaded.CurrentTurn, turn1.ID)
	}
	foundCompaction := false
	for _, ev := range reloaded.CurrentTurn.DisplayEvents {
		if ev.Role == "context_compaction" {
			foundCompaction = true
			break
		}
	}
	if !foundCompaction {
		t.Fatalf("expected persisted context_compaction display event on turn %s, got %+v", turn1.ID, reloaded.CurrentTurn.DisplayEvents)
	}
}
