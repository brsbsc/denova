package session

import (
	"context"
	"os"
	"strings"
	"testing"

	agentschema "github.com/alfredxw/denova/agent/schema"
)

func TestHistorySearchCacheTracksExternalMutationsWithoutOpeningSessions(t *testing.T) {
	dir := t.TempDir()
	writer, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	sess, err := writer.GetOrCreate("search")
	if err != nil {
		t.Fatal(err)
	}
	var revision uint64
	appendSegmentedHistoryRun(t, sess, "old", 260, &revision)
	reader, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	search := func(query string, found bool) {
		t.Helper()
		excerpt, err := reader.SearchHistoryContent(context.Background(), "search", query)
		if err != nil || (excerpt != "") != found {
			t.Fatalf("query=%q excerpt=%q err=%v", query, excerpt, err)
		}
		if found && !strings.Contains(strings.ToLower(excerpt), query) {
			t.Fatal("excerpt lost query")
		}
	}
	search("answer-01-old", true)
	search("think-00-old", false)
	search("new beacon", false)
	if len(reader.cache) != 0 {
		t.Fatal("search materialized an execution Session")
	}
	if err := sess.Append(agentschema.UserMessage("new Beacon")); err != nil {
		t.Fatal(err)
	}
	search("new beacon", true)
	if err := sess.AppendDisplayEvent(DisplayEvent{ID: "preview", Role: "assistant", Content: "discardable preview"}); err != nil {
		t.Fatal(err)
	}
	search("discardable preview", true)
	if err := sess.DiscardDisplayEvents([]string{"preview"}); err != nil {
		t.Fatal(err)
	}
	search("discardable preview", false)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sess.filePath); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.SearchHistoryContent(context.Background(), "search", "new beacon"); !os.IsNotExist(err) {
		t.Fatalf("deleted journal returned cached content: %v", err)
	}
}

func TestHistorySearchHandlesCaseExpandingUnicodeRunes(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sess, err := store.GetOrCreate("unicode-search")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("İ", 32) + " TargetAnchor " + strings.Repeat("İ", 32)
	if err := sess.Append(agentschema.UserMessage(body)); err != nil {
		t.Fatal(err)
	}
	excerpt, err := store.SearchHistoryContent(context.Background(), "unicode-search", "  TARGETANCHOR  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(excerpt, "TargetAnchor") {
		t.Fatalf("excerpt = %q, want TargetAnchor", excerpt)
	}
}
