package automationapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"denova/config"
	"denova/internal/agents/session"
	apptask "denova/internal/app/task"
	"denova/internal/automation"
	"denova/internal/book"
	projectdomain "denova/internal/project"
)

func TestAutoRunInboxConfirmationRaceDoesNotDuplicateRunOrDeadlockWorkspaceTriggers(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	chaptersDir := filepath.Join(workspace, "chapters")
	if err := os.MkdirAll(chaptersDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if err := os.WriteFile(filepath.Join(chaptersDir, filepath.Base(workspace)+"-ch"+string(rune('0'+i))+".md"), []byte("Chapter prose content."), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := session.NewStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &automationWorkspaceSnapshot{
		projectID:    "proj-race",
		projectType:  projectdomain.TypeBook,
		stateRoot:    filepath.Join(root, "store"),
		workspace:    workspace,
		novaDir:      root,
		cfg:          config.Config{},
		bookService:  book.NewService(workspace),
		sessionStore: sessions,
	}
	store := storeForSnapshot(snap)

	taskA, err := store.Create(automation.TaskDefinition{
		Scope:    automation.ScopeWorkspace,
		Target:   automation.ExecutionTarget{Kind: automation.TargetKindWorkspace, ProjectID: snap.projectID},
		Enabled:  true,
		Name:     "Batch review",
		Template: automation.TemplateReview,
		Prompt:   "Review batch",
		Triggers: []automation.TriggerDefinition{{
			ID:               "batch_1",
			Type:             automation.TriggerTypeChapterBatch,
			Enabled:          true,
			ChapterBatchSize: 2,
			NotifyPolicy:     automation.NotifyPolicyInbox,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cst := time.FixedZone("CST", 8*3600)
	nowLocal := time.Date(2026, 5, 4, 2, 5, 0, 0, cst)
	taskB, err := store.Create(automation.TaskDefinition{
		Scope:    automation.ScopeWorkspace,
		Target:   automation.ExecutionTarget{Kind: automation.TargetKindWorkspace, ProjectID: snap.projectID},
		Enabled:  true,
		Name:     "Daily summary",
		Template: automation.TemplateCustomPrompt,
		Prompt:   "Summarize daily work",
		Triggers: []automation.TriggerDefinition{{
			ID:           "schedule",
			Type:         automation.TriggerTypeSchedule,
			Enabled:      true,
			NotifyPolicy: automation.NotifyPolicySilent,
			Schedule:     automation.Schedule{Kind: automation.ScheduleDaily, Hour: 2, Minute: 0},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Seed last matched at 01:30 CST stored in UTC (17:30 previous UTC day).
	if _, err := store.UpdateTriggerState(taskB.CatalogID, "schedule", automation.TriggerState{
		LastMatchedAt: time.Date(2026, 5, 3, 17, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	host := &deliveryTestHost{fail: true}
	service := NewService(host)

	// Tick 1 while Agent is busy: taskA creates inbox item with pending auto-run; both retry cleanly.
	items, runs, err := service.processTriggers(t.Context(), snap, "", nowLocal, "test_tick_1")
	if err != nil {
		t.Fatalf("processTriggers tick 1 returned unexpected fatal error: %v", err)
	}
	if len(items) != 1 || len(runs) != 0 {
		t.Fatalf("tick 1 items=%d runs=%d, want 1 item and 0 completed runs", len(items), len(runs))
	}
	inboxItem := items[0]
	if inboxItem.Status != automation.InboxStatusAutoRun || inboxItem.RunID == "" {
		t.Fatalf("expected auto_run inbox item bound to plan.RunID, got %+v", inboxItem)
	}

	// Agent becomes available and user clicks Confirm before background scheduler tick runs.
	host.fail = false
	var workers []*apptask.Task
	var releases []chan struct{}
	starter := func(ctx context.Context, taskID, trigger, sourceRunID, runID string, evidence []automation.TriggerEvidence) (*apptask.Task, automation.RunRecord, error) {
		worker, run, startErr := service.startTaskWithSourceRunID(ctx, snap, taskID, trigger, sourceRunID, runID, evidence)
		if worker != nil {
			workers = append(workers, worker)
			releases = append(releases, host.accepted.release)
		}
		return worker, run, startErr
	}
	confirmed, err := service.confirmInboxItemWithStarter(t.Context(), store, snap, inboxItem.ID, starter)
	if err != nil {
		t.Fatalf("confirmInboxItemWithStarter failed: %v", err)
	}
	if confirmed.Run == nil || confirmed.Run.ID != inboxItem.RunID {
		t.Fatalf("confirm spawned different run: got %+v want %s", confirmed.Run, inboxItem.RunID)
	}

	// Tick 2: taskA must settle cleanly without spawning a duplicate run or deadlocking taskB!
	_, runsTick2, err := service.processTriggers(t.Context(), snap, "", nowLocal, "test_tick_2")
	if err != nil {
		t.Fatalf("processTriggers tick 2 failed: %v", err)
	}
	if len(runsTick2) != 2 {
		t.Fatalf("tick 2 runs = %d, want 2 (reconciled taskA + newly fired local-schedule taskB)", len(runsTick2))
	}
	// Release any background test workers started during tick 2.
	if host.accepted != nil {
		releases = append(releases, host.accepted.release)
	}

	// Only 2 actual Agent executions should have been started (1 for taskA, 1 for taskB):
	// host.calls had 2 failed attempts in tick 1 + 1 accepted for taskA + 1 accepted for taskB = 4 total calls.
	if len(host.calls) != 4 {
		t.Fatalf("host.calls = %d, want 4 (2 busy failures + 1 taskA + 1 taskB)", len(host.calls))
	}

	// Idle tick 3: tasks.json must NOT be rewritten when nothing matches!
	tasksFile := filepath.Join(snap.stateRoot, "automations", "tasks.json")
	beforeIdleBytes, err := os.ReadFile(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	beforeIdleInfo, err := os.Stat(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if _, _, err := service.processTriggers(t.Context(), snap, "", nowLocal.Add(time.Minute), "idle_tick"); err != nil {
		t.Fatalf("idle tick failed: %v", err)
	}
	afterIdleInfo, err := os.Stat(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	afterIdleBytes, err := os.ReadFile(tasksFile)
	if err != nil {
		t.Fatal(err)
	}
	if !afterIdleInfo.ModTime().Equal(beforeIdleInfo.ModTime()) || string(afterIdleBytes) != string(beforeIdleBytes) {
		t.Fatal("idle trigger sweep unnecessarily rewrote tasks.json")
	}
	_ = taskA

	closed := make(map[chan struct{}]bool)
	for _, ch := range releases {
		if ch != nil && !closed[ch] {
			closed[ch] = true
			close(ch)
		}
	}
	for _, w := range workers {
		<-w.Done()
	}
}
