package automation

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleEvaluatesInLocalTimezoneAcrossUTCDayBoundary(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)

	// 01:30 CST on 2026-05-04 is 17:30 UTC on 2026-05-03 (different UTC calendar day!).
	lastUTC := time.Date(2026, 5, 3, 17, 30, 0, 0, time.UTC)
	// 02:05 CST on 2026-05-04 is 18:05 UTC on 2026-05-03.
	nowLocal := time.Date(2026, 5, 4, 2, 5, 0, 0, cst)

	daily := Schedule{Kind: ScheduleDaily, Hour: 2, Minute: 0}
	if !ScheduleDue(nowLocal, lastUTC, daily) {
		t.Fatal("daily schedule at 02:00 local was not due when last was stored in UTC on previous UTC date")
	}

	// 2026-05-04 is Monday (Weekday = 1).
	weekly := Schedule{Kind: ScheduleWeekly, Weekday: int(time.Monday), Hour: 2, Minute: 0}
	if !ScheduleDue(nowLocal, lastUTC, weekly) {
		t.Fatal("weekly schedule on Monday 02:00 local was not due when last was stored in UTC on Sunday")
	}

	// Monthly across UTC month boundary: 2026-06-01 02:05 CST = 2026-05-31 18:05 UTC.
	monthLastUTC := time.Date(2026, 5, 31, 17, 30, 0, 0, time.UTC) // 2026-06-01 01:30 CST
	monthNowLocal := time.Date(2026, 6, 1, 2, 5, 0, 0, cst)
	monthly := Schedule{Kind: ScheduleMonthly, DayOfMonth: 1, Hour: 2, Minute: 0}
	if !ScheduleDue(monthNowLocal, monthLastUTC, monthly) {
		t.Fatal("monthly schedule on day 1 at 02:00 local was not due across UTC month boundary")
	}
}

func TestDisabledAndArchivedTasksRejectAutomaticRunsAndDeleteResolvesAuthoritativeLedger(t *testing.T) {
	root := t.TempDir()
	store := NewProjectStore(filepath.Join(root, "nova"), "proj-1", filepath.Join(root, "book"), filepath.Join(root, "store"))

	// Invalid trigger schedule must be rejected during Create/NormalizeTask.
	if _, err := store.Create(TaskDefinition{
		Scope:    ScopeWorkspace,
		Target:   ExecutionTarget{Kind: TargetKindWorkspace, ProjectID: "proj-1"},
		Name:     "Bad schedule",
		Template: TemplateCustomPrompt,
		Triggers: []TriggerDefinition{{
			Type:     TriggerTypeSchedule,
			Enabled:  true,
			Schedule: Schedule{Kind: ScheduleDaily, Hour: 99, Minute: 0},
		}},
	}); err == nil {
		t.Fatal("expected invalid trigger schedule hour to fail")
	}

	task, err := store.Create(TaskDefinition{
		Scope:    ScopeWorkspace,
		Target:   ExecutionTarget{Kind: TargetKindWorkspace, ProjectID: "proj-1"},
		Enabled:  false,
		Name:     "Guarded task",
		Template: TemplateCustomPrompt,
		Prompt:   "Check chapter",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.AppendRun(task.CatalogID, RunRecord{
		ID:             "run-disabled-schedule",
		TaskID:         task.ID,
		Trigger:        TriggerSchedule,
		Input:          &RunInput{Message: "Check chapter"},
		DeliveryStatus: DeliveryPending,
		StartedAt:      time.Now().UTC(),
	}); err == nil || !strings.Contains(err.Error(), "is disabled") {
		t.Fatalf("expected disabled task to reject automatic schedule run, got %v", err)
	}

	// Simulate an interrupted cleanup: history has settled terminal run at Revision 2,
	// while run-obligations still holds stale DeliveryPending at Revision 1.
	runID := "run-settled-crash"
	if err := store.writeDurableRunObligation(ScopeWorkspace, durableRunFile{
		Version:       durableRunFileVersion,
		Revision:      1,
		TaskCatalogID: task.CatalogID,
		Run: RunRecord{
			ID:             runID,
			TaskID:         task.ID,
			Trigger:        TriggerManual,
			DeliveryStatus: DeliveryPending,
			StartedAt:      time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.writeDurableRun(ScopeWorkspace, durableRunFile{
		Version:       durableRunFileVersion,
		Revision:      2,
		TaskCatalogID: task.CatalogID,
		Run: RunRecord{
			ID:             runID,
			TaskID:         task.ID,
			Trigger:        TriggerManual,
			Status:         RunStatusSuccess,
			DeliveryStatus: DeliveryAccepted,
			StartedAt:      time.Now().UTC(),
			FinishedAt:     time.Now().UTC(),
		},
	}); err != nil {
		t.Fatal(err)
	}

	latest, err := store.GetInScope(ScopeWorkspace, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteInScopeIfRevision(ScopeWorkspace, task.ID, latest.Revision); err != nil {
		t.Fatalf("DeleteInScopeIfRevision failed on stale obligation shadowed by settled history: %v", err)
	}

	if _, err := store.AppendRun(task.CatalogID, RunRecord{
		ID:             "run-after-archive",
		TaskID:         task.ID,
		Trigger:        TriggerManual,
		Input:          &RunInput{Message: "Check chapter"},
		DeliveryStatus: DeliveryPending,
		StartedAt:      time.Now().UTC(),
	}); !errors.Is(err, ErrTaskArchived) {
		t.Fatalf("expected ErrTaskArchived on AppendRun after archive, got %v", err)
	}
}

func TestAutoRunInboxItemsCanBeDismissedAndRemainBounded(t *testing.T) {
	root := t.TempDir()
	store := NewProjectStore(filepath.Join(root, "nova"), "proj-1", filepath.Join(root, "book"), filepath.Join(root, "store"))

	now := time.Now().UTC()
	var createdFirstID string
	for i := range MaxInboxItems + 10 {
		item, err := store.CreateInboxItem(TriggerInboxItem{
			ID:           fmt.Sprintf("inbox-auto-%d", i),
			TaskID:       "task-1",
			TriggerID:    "semantic_1",
			Scope:        ScopeWorkspace,
			ProjectID:    "proj-1",
			Status:       InboxStatusAutoRun,
			RunID:        fmt.Sprintf("run-%d", i),
			ActionPolicy: ActionPolicyAutoRun,
			NotifyPolicy: NotifyPolicyInbox,
			Title:        "Matched",
			Fingerprint:  fmt.Sprintf("fp-%d", i),
			CreatedAt:    now.Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == MaxInboxItems+9 {
			createdFirstID = item.ID
		}
	}

	items, err := store.ListInbox()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != MaxInboxItems {
		t.Fatalf("ListInbox length = %d, want bounded %d", len(items), MaxInboxItems)
	}

	dismissed, err := store.DismissInboxItem(createdFirstID)
	if err != nil || dismissed.Status != InboxStatusDismissed {
		t.Fatalf("DismissInboxItem on auto_run item = (%+v, %v)", dismissed, err)
	}
}
