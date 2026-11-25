package execution

import (
	"context"
	"testing"
	"time"

	agentchat "denova/internal/agents/chat"
	agentconversation "denova/internal/agents/conversation"
	agentrun "denova/internal/agents/run"
	"denova/internal/agents/session"

	"github.com/alfredxw/denova/agent"
	agentevent "github.com/alfredxw/denova/agent/lifecycle/event"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

func TestRecoveryObservationWaitHandlesClosedErrorChannel(t *testing.T) {
	events := make(chan agentevent.Event, 2)
	errs := make(chan error)
	close(errs)

	var emitted []agentrun.Event
	obs := &RecoveryObservation{
		publicBackend: &publicBackend{},
		publicSession: &agent.Session{},
		publicInitial: agentevent.SessionSnapshot{ActiveRunID: "run-active", Cursor: 1},
		publicObservation: agentevent.Observation{
			Events: events,
			Errors: errs,
		},
	}

	go func() {
		defer func() { _ = recover() }()
		time.Sleep(15 * time.Millisecond)
		events <- agentevent.Event{
			Cursor:  2,
			RunID:   "run-active",
			Payload: agentevent.RunSettled{Status: agentschema.ResultCompleted},
		}
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	outcome := obs.Wait(ctx, func(ev agentrun.Event) {
		emitted = append(emitted, ev)
	})
	if outcome.Status != agentrun.OutcomeCompleted {
		t.Fatalf("outcome = %#v, want completed", outcome)
	}
	if len(emitted) == 0 || emitted[len(emitted)-1].Type != "done" {
		t.Fatalf("expected terminal done event, got %#v", emitted)
	}
}

func TestPublicBackendWaitFlushesProjectorWhenCallerContextCancelled(t *testing.T) {
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sess, err := store.GetOrCreate("cancel-flush")
	if err != nil {
		t.Fatal(err)
	}

	model := &publicBackendNextTurnModel{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	runtime := NewEphemeralRuntime()
	defer runtime.Close(context.Background())

	var events []agentrun.Event
	conv := agentconversation.NewSessionConversation(sess)
	accepted, err := runtime.Start(t.Context(), StartRequest{
		Cycle: Cycle{
			Definition:   agent.Definition{Name: "CancelFlushAgent", Model: model},
			Conversation: conv,
			Request:      agentchat.ChatRequest{CommandID: "cmd-cancel-flush", Message: "Draft scene"},
			Options:      agentrun.Options{AgentKind: agentrun.AgentKindIDE, ProjectID: "proj", SessionID: sess.ID},
		},
		Emit: func(ev agentrun.Event) {
			events = append(events, ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("model did not start")
	}

	waitCtx, cancel := context.WithCancel(t.Context())
	cancel()
	outcome := accepted.Wait(waitCtx)
	if outcome.Status != agentrun.OutcomeAborted {
		t.Fatalf("outcome = %#v, want aborted", outcome)
	}
	hasTerminal := false
	for _, ev := range events {
		if ev.Type == "aborted" || ev.Type == "error" || ev.Type == "done" {
			hasTerminal = true
			break
		}
	}
	if !hasTerminal {
		t.Fatalf("projector was not flushed on cancelled Wait context: %#v", events)
	}
}
