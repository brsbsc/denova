package state

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestStoreRejectsNonPortableAndCaseCollidingPaths(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base, err := store.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		"C:foo.md",
		"C:/foo.md",
		".GIT/config",
		".Git/transaction.json",
		"CON.md",
		"prompts/aux.txt",
		"prompts/trailing.",
		"prompts/trailing ",
	} {
		if _, err := base.Read(bad); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Snapshot.Read(%q) err = %v, want ErrInvalidPath", bad, err)
		}
		var validationErr *ValidationError
		if _, err := store.Write(ctx, ChangeSet{
			BaseRevision: base.Revision,
			Changes:      []Change{{Path: bad, Content: []byte("x")}},
		}); !errors.As(err, &validationErr) {
			t.Fatalf("store.Write(%q) err = %v, want *ValidationError", bad, err)
		}
	}

	written, err := store.Write(ctx, ChangeSet{
		BaseRevision: base.Revision,
		Changes:      []Change{{Path: "prompts/Readme.md", Content: []byte("v1")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, ChangeSet{
		BaseRevision: written.Snapshot.Revision,
		Changes:      []Change{{Path: "prompts/readme.md", Content: []byte("v2")}},
	}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected case-folding collision error, got %v", err)
	}
}

func TestAppliedTransactionRecoveryCompletesForwardToCandidate(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base, err := store.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := applyChanges(base, []Change{{Path: "prompts/committed.md", Content: []byte("committed")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.cacheSnapshot(base); err != nil {
		t.Fatal(err)
	}
	if err := store.cacheSnapshot(candidate); err != nil {
		t.Fatal(err)
	}
	// Simulate crash after Stage="applied" was persisted even if working tree was stale.
	marker := store.transactionPath()
	if err := atomicJSON(marker, transactionRecord{
		BaseRevision: base.Revision, CandidateRevision: candidate.Revision, Stage: "applied",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.recoverTransaction(); err != nil {
		t.Fatalf("recover applied transaction: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected marker removed, got %v", err)
	}
	current, err := store.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotFile(t, current, "prompts/committed.md", "committed")
}
