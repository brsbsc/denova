package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	agentengine "github.com/alfredxw/denova/agent/engine"
	agentexecution "github.com/alfredxw/denova/agent/engine/execution"
	agentgoal "github.com/alfredxw/denova/agent/engine/goal"
	agentmiddleware "github.com/alfredxw/denova/agent/engine/middleware"
	agentmodel "github.com/alfredxw/denova/agent/model"
	agentstream "github.com/alfredxw/denova/agent/model/stream"
	agentschema "github.com/alfredxw/denova/agent/schema"
	agentsession "github.com/alfredxw/denova/agent/session"
)

type failToggleStore struct {
	inner      agentsession.Store
	failAppend atomic.Bool
}

func (s *failToggleStore) Open(ctx context.Context, key agentsession.Key) (agentsession.Log, error) {
	log, err := s.inner.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	return &failToggleLog{inner: log, failAppend: &s.failAppend}, nil
}

func (s *failToggleStore) List(ctx context.Context, sel agentsession.Selector) ([]agentsession.Key, error) {
	return s.inner.List(ctx, sel)
}

func (s *failToggleStore) Delete(ctx context.Context, key agentsession.Key) error {
	return s.inner.Delete(ctx, key)
}

type failToggleLog struct {
	inner      agentsession.Log
	failAppend *atomic.Bool
}

func (l *failToggleLog) Replay(ctx context.Context, fn func(agentsession.Record) error) (agentsession.ReplayStats, error) {
	return l.inner.Replay(ctx, fn)
}

func (l *failToggleLog) Append(ctx context.Context, expected agentsession.Revision, records ...agentsession.Record) (agentsession.Revision, error) {
	if l.failAppend.Load() {
		return 0, errors.New("simulated journal append failure")
	}
	return l.inner.Append(ctx, expected, records...)
}

func (l *failToggleLog) Close() error {
	return l.inner.Close()
}

type repairDraftModel struct {
	calls atomic.Int32
}

func (*repairDraftModel) Generate(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentschema.Message, error) {
	return nil, errors.New("unexpected Generate")
}

func (m *repairDraftModel) Stream(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	if m.calls.Add(1) == 1 {
		return agentstream.StreamReaderFromArray([]*agentschema.Message{
			agentschema.AssistantMessage("rejected-draft", nil),
		}), nil
	}
	return agentstream.StreamReaderFromArray([]*agentschema.Message{
		agentschema.AssistantMessage("clean-final", nil),
	}), nil
}

type repairOnceMiddleware struct {
	agentmiddleware.BaseMiddleware
	repaired atomic.Bool
}

func (mw *repairOnceMiddleware) ReviewModelOutput(_ context.Context, _ agentmodel.ModelOutput) (agentmodel.ModelOutputReview, error) {
	if mw.repaired.CompareAndSwap(false, true) {
		return agentmodel.ModelOutputReview{
			Action: agentmodel.ModelOutputRepair,
			Reason: "draft_rejected",
			Feedback: []agentschema.ContextFragment{{
				Source:    "test.review",
				Purpose:   "repair",
				Content:   "Rewrite without draft artifacts.",
				HardLimit: 256,
			}},
		}, nil
	}
	return agentmodel.ModelOutputReview{Action: agentmodel.ModelOutputAccept}, nil
}

func TestCapabilityCommitFailureDoesNotMutateInMemoryCapabilities(t *testing.T) {
	store := &failToggleStore{inner: agentsession.Memory()}
	owner, err := New(context.Background(), agentengine.Definition{
		Name:          "goal-rollback",
		Model:         &repairDraftModel{},
		ModelIdentity: agentschema.CapabilityIdentity{Kind: "test.model", Version: 1},
		Goal:          agentgoal.Standard(),
	}, WithSessionStore(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })

	session, err := owner.Session(context.Background(), agentsession.Named("goal-rollback"))
	if err != nil {
		t.Fatal(err)
	}

	store.failAppend.Store(true)
	_, err = session.UpdateGoal(context.Background(), agentschema.GoalMutation{
		Kind:       agentschema.GoalSet,
		Objective:  "Must not leak on journal error",
		MutationID: "goal-fail-1",
	})
	if err == nil {
		t.Fatal("expected UpdateGoal to fail when journal Append fails")
	}
	if _, leaked := session.capabilities[agentgoal.GoalCapability]; leaked {
		t.Fatalf("failed UpdateGoal mutated in-memory session.capabilities: %s", session.capabilities[agentgoal.GoalCapability])
	}

	run := &Run{session: session}
	err = run.applyCapabilityUpdates([]agentengine.CapabilityState{{
		Capability: "custom.cap",
		State:      json.RawMessage(`{"v":1}`),
	}})
	if err == nil {
		t.Fatal("expected applyCapabilityUpdates to fail when journal Append fails")
	}
	if _, leaked := session.capabilities["custom.cap"]; leaked {
		t.Fatalf("failed applyCapabilityUpdates mutated in-memory session.capabilities: %s", session.capabilities["custom.cap"])
	}
}

func TestModelRetryRollsBackCompletedOutputBufferOnReviewRepair(t *testing.T) {
	owner, err := New(context.Background(), agentengine.Definition{
		Name:          "review-repair",
		Model:         &repairDraftModel{},
		ModelIdentity: agentschema.CapabilityIdentity{Kind: "test.model", Version: 1},
		Middlewares:   []agentmiddleware.Middleware{&repairOnceMiddleware{}},
		Execution:     agentexecution.ExecutionPolicy{ModelMaxAttempts: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })

	session, err := owner.Session(context.Background(), agentsession.Named("review-repair"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := session.Run(context.Background(), agentschema.Text("draft"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Wait(context.Background())
	if err != nil || result.Status != agentschema.ResultCompleted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	snapshot, err := session.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.RecentRuns) != 1 || snapshot.RecentRuns[0].Output != "clean-final" {
		t.Fatalf("expected repaired output 'clean-final' without stale draft prefix, got %#v", snapshot.RecentRuns)
	}
}

func TestSuspendAndCloseRetriesStopDescendantsWhenRootIdle(t *testing.T) {
	owner, err := New(context.Background(), agentengine.Definition{
		Name:          "tree-retry",
		Model:         &repairDraftModel{},
		ModelIdentity: agentschema.CapabilityIdentity{Kind: "test.model", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })

	session, err := owner.Session(context.Background(), agentsession.Named("tree-retry"))
	if err != nil {
		t.Fatal(err)
	}

	var stopCalls atomic.Int32
	stopDescendants := func() error {
		if stopCalls.Add(1) == 1 {
			return errors.New("descendant transiently busy")
		}
		return nil
	}

	req := SuspendRequest{IdempotencyKey: "tree-stop-1:session", Reason: "tree stop"}
	if _, err := session.suspendAndClose(context.Background(), req, stopDescendants); err == nil {
		t.Fatal("expected first suspendAndClose to fail when stopDescendants fails")
	}
	session.mu.RLock()
	closedAfterFirst := session.closed
	session.mu.RUnlock()
	if closedAfterFirst {
		t.Fatal("session closed despite failed stopDescendants")
	}

	if _, err := session.suspendAndClose(context.Background(), req, stopDescendants); err != nil {
		t.Fatalf("expected retry suspendAndClose to succeed, got %v", err)
	}
	if stopCalls.Load() != 2 {
		t.Fatalf("expected stopDescendants to run on retry, calls=%d", stopCalls.Load())
	}
	session.mu.RLock()
	closedAfterRetry := session.closed
	session.mu.RUnlock()
	if !closedAfterRetry {
		t.Fatal("expected session writer to close after successful retry")
	}
}
