package messages

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestServiceMarkReadAndMarkAllReadPersistAtomically(t *testing.T) {
	novaDir := t.TempDir()
	changelog := filepath.Join(t.TempDir(), "CHANGELOG.md")
	content := "# Changelog\n\n## [0.8.0] - 2026-11-25\n### Added\n- 新增功能 / New feature\n"
	if err := os.WriteFile(changelog, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewServiceWithChangelog(novaDir, changelog)
	msgs, err := svc.ChangelogForLocale("zh")
	if err != nil || len(msgs) == 0 {
		t.Fatalf("ChangelogForLocale failed: msgs=%#v err=%v", msgs, err)
	}

	ts, err := svc.MarkRead("msg-1")
	if err != nil || ts.IsZero() {
		t.Fatalf("MarkRead failed: ts=%v err=%v", ts, err)
	}
	tsAgain, err := svc.MarkRead("msg-1")
	if err != nil || !tsAgain.Equal(ts) {
		t.Fatalf("idempotent MarkRead mismatch: got=%v want=%v err=%v", tsAgain, ts, err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		id := "concurrent-" + string(rune('a'+i))
		go func(msgID string) {
			defer wg.Done()
			if err := svc.MarkAllRead([]string{msgID, "shared-id"}); err != nil {
				t.Errorf("MarkAllRead(%q) failed: %v", msgID, err)
			}
		}(id)
	}
	wg.Wait()

	reloaded := NewService(novaDir)
	state, err := reloaded.ReadState()
	if err != nil {
		t.Fatalf("ReadState failed: %v", err)
	}
	for _, wantID := range []string{"msg-1", "shared-id", "concurrent-a", "concurrent-f"} {
		if _, ok := state[wantID]; !ok {
			t.Fatalf("persisted state missing %q: %#v", wantID, state)
		}
	}

	entries, err := os.ReadDir(filepath.Join(novaDir, "messages"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("unexpected leftover temp file %q", entry.Name())
		}
	}
}
