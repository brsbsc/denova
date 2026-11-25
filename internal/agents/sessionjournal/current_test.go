package sessionjournal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"denova/internal/agents/conversationjournal"

	"github.com/alfredxw/denova/agent"
	agentschema "github.com/alfredxw/denova/agent/schema"
	agentsession "github.com/alfredxw/denova/agent/session"
)

func TestCurrentProjectionCheckpointHandlesSuspendedSettlements(t *testing.T) {
	key := agentsession.Named("suspended-session")
	canonical, err := agentsession.CanonicalKey(key)
	if err != nil {
		t.Fatal(err)
	}

	var current CurrentProjection
	current.Reset()
	current.Streams[canonical] = &streamProjection{
		Start: recordLocator{Cursor: 1},
		Key:   key,
		Recovery: agent.RecoveryIndex{
			Revision: 2,
			Recent: []agent.RecoveryRun{
				{Status: agentschema.ResultSuspended, Settlement: 0},
				{Status: agentschema.ResultSuspended, Settlement: 5},
				{Status: agentschema.ResultCompleted, Settlement: 2},
			},
		},
		Locations: map[agentsession.Revision]recordLocator{
			2: {Cursor: 2},
		},
	}

	checkpoint, err := current.Checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint() failed on suspended settlements: %v", err)
	}
	var restored CurrentProjection
	if err := restored.Restore(checkpoint); err != nil {
		t.Fatalf("Restore() failed: %v", err)
	}
	cursors := current.CurrentReadCursors()
	if len(cursors) != 1 || cursors[0] != 2 {
		t.Fatalf("CurrentReadCursors() = %v, want [2]", cursors)
	}

	journalPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(journalPath, []byte("{\"type\":\"header\",\"id\":\"suspended\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := conversationjournal.Identity{ID: "suspended", Generation: "gen-1"}
	writer, err := conversationjournal.Open(t.Context(), journalPath, identity, &CurrentProjection{}, conversationjournal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	startedPayload, err := json.Marshal(Envelope{
		Type: RecordType, Key: key, Revision: 1, Kind: turnStartedKind, Version: 1,
		Data: json.RawMessage(`{"run_id":"run-1","command_id":"cmd-1","at":"2026-01-01T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	interruptedPayload, err := json.Marshal(Envelope{
		Type: RecordType, Key: key, Revision: 2, Kind: turnInterruptedKind, Version: 1,
		Data: json.RawMessage(`{"run_id":"run-1","command_id":"cmd-1","at":"2026-01-01T00:00:01Z","reason":"user_requested"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	head := writer.Head()
	if _, err := writer.Append(t.Context(), conversationjournal.Guard{Cursor: head.Cursor, RecordSHA256: head.RecordSHA256}, startedPayload, interruptedPayload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := OpenCurrent(t.Context(), journalPath, identity, key)
	if err != nil {
		t.Fatalf("OpenCurrent() failed after interrupted turn: %v", err)
	}
	defer reader.Close()
	var count int
	if _, err := reader.Replay(t.Context(), func(agentsession.Record) error {
		count++
		return nil
	}); err != nil || count == 0 {
		t.Fatalf("OpenCurrent Replay = %d records, err=%v", count, err)
	}
}
