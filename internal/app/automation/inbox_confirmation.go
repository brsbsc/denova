package automationapp

import (
	"context"
	"fmt"
	"strings"

	apptask "denova/internal/app/task"
	"denova/internal/automation"
)

type automationInboxRunStarter func(context.Context, string, string, string, string, []automation.TriggerEvidence) (*apptask.Task, automation.RunRecord, error)

func (s *Service) confirmInboxItemWithStarter(ctx context.Context, store *automation.Store, snap *automationWorkspaceSnapshot, id string, start automationInboxRunStarter) (automation.InboxActionResult, error) {
	item, err := store.GetInboxItem(id)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	taskID := automation.CatalogTaskID(item.Scope, item.Workspace, item.TaskID)
	if projectID := strings.TrimSpace(item.ProjectID); item.Scope == automation.ScopeWorkspace && projectID != "" {
		taskID = projectID + ":" + strings.TrimSpace(item.TaskID)
	}
	task, err := store.Get(taskID)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	if start == nil {
		return automation.InboxActionResult{}, fmt.Errorf("automation inbox run starter is required")
	}
	if item.Status == automation.InboxStatusAutoRun && strings.TrimSpace(item.RunID) != "" {
		autoRunID := strings.TrimSpace(item.RunID)
		persisted, found, lookupErr := persistedAutomationRunByID(storeForSnapshot(snap), autoRunID)
		if lookupErr != nil {
			return automation.InboxActionResult{}, lookupErr
		}
		if found {
			if validateAutomationRunRootReceipt(persisted) == nil {
				return automation.InboxActionResult{Item: item, Run: &persisted}, nil
			}
			_, run, startErr := start(ctx, automationTaskStoreID(task), persisted.Trigger, persisted.SourceRunID, persisted.ID, item.Evidence)
			if startErr != nil {
				return automation.InboxActionResult{}, startErr
			}
			if run.ID != autoRunID {
				return automation.InboxActionResult{}, fmt.Errorf("automation inbox auto-run identity changed: got %s want %s", run.ID, autoRunID)
			}
			if err := validateAutomationRunRootReceipt(run); err != nil {
				return automation.InboxActionResult{}, fmt.Errorf("automation inbox auto-run %s has no valid durable root receipt: %w", run.ID, err)
			}
			return automation.InboxActionResult{Item: item, Run: &run}, nil
		}
		return automation.InboxActionResult{}, fmt.Errorf("%w: inbox_id=%s auto-run %s is already claimed", automation.ErrTriggerActionConflict, item.ID, autoRunID)
	}
	runID, err := automation.InboxConfirmationRunID(item)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	claimed, _, err := store.ClaimInboxRun(ctx, item.ID, runID)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	trigger := automation.TriggerInboxConfirmation
	sourceRunID := ""
	if claimed.Purpose == automation.InboxPurposeWriteConfirmation {
		trigger = automation.TriggerWriteConfirmation
		sourceRunID = claimed.SourceRunID
	}
	_, run, err := start(ctx, automationTaskStoreID(task), trigger, sourceRunID, runID, claimed.Evidence)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	if run.ID != runID {
		return automation.InboxActionResult{}, fmt.Errorf("automation inbox run identity changed: got %s want %s", run.ID, runID)
	}
	if err := validateAutomationRunRootReceipt(run); err != nil {
		return automation.InboxActionResult{}, fmt.Errorf("automation inbox run %s has no valid durable root receipt: %w", run.ID, err)
	}

	updated, err := store.CompleteInboxRun(ctx, claimed.ID, run.ID)
	if err != nil {
		return automation.InboxActionResult{}, err
	}
	return automation.InboxActionResult{Item: updated, Run: &run}, nil
}
