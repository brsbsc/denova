package file

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alfredxw/denova/agent/session"
)

func TestOpenReaderValidatesStoreContextAndManifest(t *testing.T) {
	var nilStore *Store
	if _, err := nilStore.OpenReader(context.Background(), session.Named("s1")); err == nil {
		t.Fatal("expected nil Store.OpenReader to fail")
	}

	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.OpenReader(canceled, session.Named("s1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}

	if _, err := store.OpenReader(context.Background(), session.Named("missing")); err == nil {
		t.Fatal("expected OpenReader on missing session manifest to fail")
	}

	key := session.Named("valid-session")
	writer, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := writer.Append(context.Background(), 0, session.Record{
		Kind:    "test.event",
		Version: 1,
		Data:    json.RawMessage(`{"ok":true}`),
	})
	if err != nil || rev != 1 {
		t.Fatalf("Append rev=%d err=%v", rev, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	reader, err := store.OpenReader(context.Background(), key)
	if err != nil {
		t.Fatalf("OpenReader failed on valid session: %v", err)
	}
	defer reader.Close()

	if indexed, ok := reader.(interface {
		ReadRecord(context.Context, session.Revision) (session.Record, error)
	}); ok {
		rec, err := indexed.ReadRecord(context.Background(), 1)
		if err != nil || rec.Kind != "test.event" {
			t.Fatalf("ReadRecord rec=%#v err=%v", rec, err)
		}
	}

	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := store.OpenReader(context.Background(), key); err == nil {
		t.Fatal("expected OpenReader to fail after Delete removed manifest")
	}
}
