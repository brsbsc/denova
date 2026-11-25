package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentcompaction "github.com/alfredxw/denova/agent/context/compaction"
	agenthistory "github.com/alfredxw/denova/agent/context/history"
	agentexecution "github.com/alfredxw/denova/agent/engine/execution"
	agentmodel "github.com/alfredxw/denova/agent/model"
	agentstream "github.com/alfredxw/denova/agent/model/stream"
	agentschema "github.com/alfredxw/denova/agent/schema"
	agentsession "github.com/alfredxw/denova/agent/session"
	agentcanonical "github.com/alfredxw/denova/agent/session/canonical"
)

type regressionStaticModel struct {
	reply string
}

func (m *regressionStaticModel) Generate(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentschema.Message, error) {
	return agentschema.AssistantMessage(m.reply, nil), nil
}

func (m *regressionStaticModel) Stream(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	return agentstream.StreamReaderFromArray([]*agentschema.Message{agentschema.AssistantMessage(m.reply, nil)}), nil
}

type regressionCanonicalHost struct {
	facts map[string]PersistedTool
}

func (h *regressionCanonicalHost) CommitCanonical(_ context.Context, _ CanonicalUpdate, apply func(agentcanonical.CanonicalCheckpoint) error) error {
	if apply != nil {
		return apply(nil)
	}
	return nil
}

func (h *regressionCanonicalHost) ToolFacts() map[string]PersistedTool {
	return h.facts
}

type noopCompactionManager struct{}

func (noopCompactionManager) Identity() agentschema.CapabilityIdentity {
	return agentschema.CapabilityIdentity{Kind: "test.compaction.noop", Version: 1}
}

func (noopCompactionManager) SummaryLimitBytes() int { return 4096 }

func (noopCompactionManager) Plan(context.Context, agentcompaction.CompactionPlanRequest) (agenthistory.CompactionPlan, error) {
	return agenthistory.CompactionPlan{}, nil
}

func (noopCompactionManager) Compact(context.Context, agentcompaction.CompactionCompactRequest) (agentcompaction.CompactionCheckpoint, error) {
	return agentcompaction.CompactionCheckpoint{Summary: "unused"}, nil
}

func TestRestorePendingToolBatchWithHistoryArchive(t *testing.T) {
	key := agentsession.Named("archived-tool-recovery")
	eng, err := New(Config{
		Source: Definition{
			Name:          "writer",
			Model:         &regressionStaticModel{reply: "ok"},
			ModelIdentity: agentschema.CapabilityIdentity{Kind: "test.model", Version: 1},
		},
		Session: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareDefinitionBase(context.Background(), eng.source, PrepareRequest{
		Session: agentschema.SessionView{Key: eng.key},
	})
	if err != nil {
		t.Fatal(err)
	}

	assistant := agentschema.AssistantMessage("", []agentschema.ToolCall{
		{ID: "call-a", Type: "function", Function: agentschema.FunctionCall{Name: "read", Arguments: `{}`}},
		{ID: "call-b", Type: "function", Function: agentschema.FunctionCall{Name: "read", Arguments: `{}`}},
	})
	assistant.AgentMeta = &agentschema.AgentMessageMeta{ModelResponseOrdinal: 1}
	accepted := agentschema.TextToolResult("accepted-a")

	state := engineTranscript{
		Version: engineTranscriptVersion,
		Archive: &agenthistory.HistoryArchive{From: 0, To: 4},
		Messages: []*agentschema.Message{
			agentschema.UserMessage("kept user"),
			agentschema.AssistantMessage("kept assistant", nil),
			assistant,
			agentschema.ToolMessage(accepted, "call-a", agentschema.WithToolName("read")),
		},
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := CanonicalMessageCheckpoint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.MessageCount <= len(state.Messages) {
		t.Fatalf("expected raw checkpoint count (%d) > local messages (%d)", checkpoint.MessageCount, len(state.Messages))
	}

	scope, err := agentsession.CanonicalKey(eng.key)
	if err != nil {
		t.Fatal(err)
	}
	namespace := agentexecution.RootToolNamespace(agentexecution.InvocationIdentity{
		Scope: scope, OperationID: "run-1", Cycle: 1,
	}, "writer")
	execA := agentexecution.ExecutionIDForNamespace(namespace, 1, 0)
	host := &regressionCanonicalHost{
		facts: map[string]PersistedTool{
			execA: {Started: true, Result: &accepted},
		},
	}

	err = eng.restorePendingToolBatch(context.Background(), Request{
		Journal: host,
		Snapshot: TurnSnapshot{
			OperationID: "run-1",
			Cycle:       1,
			State:       encoded,
		},
	}, &prepared, &state, func(Event) error { return nil })
	if err != nil {
		t.Fatalf("restorePendingToolBatch returned unexpected error: %v", err)
	}
	if len(state.Messages) != 5 {
		t.Fatalf("expected 5 local messages after restore, got %d", len(state.Messages))
	}
	if state.Messages[0].Content != "kept user" || state.Messages[1].Content != "kept assistant" {
		t.Fatalf("committed prefix corrupted: %#v", state.Messages[:2])
	}
	if state.Messages[3].Content != "accepted-a" {
		t.Fatalf("expected confirmed tool result for call-a, got %#v", state.Messages[3])
	}
	if state.Messages[4].ToolResult == nil || state.Messages[4].ToolResult.Status != agentschema.ToolResultSkipped {
		t.Fatalf("expected skipped tool result for unstarted call-b, got %#v", state.Messages[4])
	}
}

func TestEngineRunClonesCallerSnapshotCapabilities(t *testing.T) {
	key := agentsession.Named("clone-snapshot-caps")
	eng, err := New(Config{
		Source: Definition{
			Name:          "compactor",
			Model:         &regressionStaticModel{reply: "done"},
			ModelIdentity: agentschema.CapabilityIdentity{Kind: "test.model", Version: 1},
			Compaction:    noopCompactionManager{},
		},
		Session: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, userInput, err := EncodeInput(agentschema.Text("hello"))
	if err != nil {
		t.Fatal(err)
	}
	initialHealth := json.RawMessage(`{"consecutive_failures":1,"failure_code":"transient","fingerprint":"fp-1"}`)
	callerCaps := map[string]json.RawMessage{
		agenthistory.CompactionHealthCapability: append(json.RawMessage(nil), initialHealth...),
	}
	result, err := eng.Run(context.Background(), Request{
		Journal: &regressionCanonicalHost{},
		Snapshot: TurnSnapshot{
			ID:           "snap-1",
			CommandID:    "cmd-1",
			OperationID:  "run-1",
			Cycle:        1,
			StartedAt:    time.Now().UTC(),
			Delivery:     DeliveryStart,
			Input:        userInput,
			Capabilities: callerCaps,
		},
	}, func(Event) error { return nil })
	if err != nil || result.Status != Completed {
		t.Fatalf("Run result=%#v err=%v", result, err)
	}
	got, present := callerCaps[agenthistory.CompactionHealthCapability]
	if !present || string(got) != string(initialHealth) {
		t.Fatalf("Engine.Run mutated caller Snapshot.Capabilities map: present=%v got=%s", present, got)
	}
}
