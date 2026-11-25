package versions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	workspacechange "denova/internal/workspace/change"
)

func TestWithProtectedExcludedWorkspaceDirsPreservesBackupOnRestoreFailure(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewService(workspace, repoDir)
	t.Cleanup(service.Close)
	v1, err := service.Create("v1", VersionSourceManual, DefaultAutoSettings())
	if err != nil || v1.Version == nil {
		t.Fatalf("create v1: %v", err)
	}

	storyFile := filepath.Join(workspace, ".denova", "interactive", "story-1.jsonl")
	reviewFile := filepath.Join(workspace, ".denova", "reviews", "thread-1.json")
	if err := os.MkdirAll(filepath.Dir(storyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(reviewFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storyFile, []byte("canonical-story-journal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reviewFile, []byte(`{"id":"thread-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate a restore failure where .denova is blocked by a regular file so
	// restoreProtectedDirMoves cannot move staged directories back into place.
	err = service.withProtectedExcludedWorkspaceDirs(func() error {
		entries, readErr := os.ReadDir(workspace)
		if readErr != nil {
			return readErr
		}
		stagedInsideWorkspace := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), versionRestoreStagingPrefix) {
				stagedInsideWorkspace = true
			}
		}
		if !stagedInsideWorkspace {
			t.Fatal("expected protected .denova dirs to be staged inside s.workspace")
		}
		_ = os.Remove(filepath.Join(workspace, ".denova"))
		return os.WriteFile(filepath.Join(workspace, ".denova"), []byte("blocker"), 0o644)
	})
	if err == nil {
		t.Fatal("expected withProtectedExcludedWorkspaceDirs to fail when .denova is blocked")
	}

	// Verify none of the protected backups were deleted from the staging dir.
	var foundStory, foundReview bool
	_ = filepath.WalkDir(workspace, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == "story-1.jsonl" {
			data, readErr := os.ReadFile(path)
			if readErr == nil && string(data) == "canonical-story-journal\n" {
				foundStory = true
			}
		}
		if filepath.Base(path) == "thread-1.json" {
			data, readErr := os.ReadFile(path)
			if readErr == nil && string(data) == `{"id":"thread-1"}` {
				foundReview = true
			}
		}
		return nil
	})
	if !foundStory || !foundReview {
		t.Fatalf("protected backups were lost on restore failure: foundStory=%t foundReview=%t", foundStory, foundReview)
	}

	// Remove the blocker file and verify version Status ignores the retained staging directory.
	if err := os.Remove(filepath.Join(workspace, ".denova")); err != nil {
		t.Fatal(err)
	}
	status, err := service.Status(context.Background(), DefaultAutoSettings())
	if err != nil {
		t.Fatalf("Status with retained staging dir failed: %v", err)
	}
	if !status.Clean {
		t.Fatalf("staging dir leaked into visible version status: %#v", status.Changes)
	}
}

func TestRestoreWorkspaceCleansStagingDirOnSuccess(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("version one"), 0o644); err != nil {
		t.Fatal(err)
	}
	storyFile := filepath.Join(workspace, ".denova", "interactive", "story-main.jsonl")
	if err := os.MkdirAll(filepath.Dir(storyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storyFile, []byte("keep-across-restore\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := NewService(workspace, repoDir)
	t.Cleanup(service.Close)
	v1, err := service.Create("v1", VersionSourceManual, DefaultAutoSettings())
	if err != nil || v1.Version == nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("version two"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Restore(v1.Version.ID, DefaultAutoSettings()); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}
	gotStory, err := os.ReadFile(storyFile)
	if err != nil || string(gotStory) != "keep-across-restore\n" {
		t.Fatalf("story journal after restore = %q, err=%v", gotStory, err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), versionRestoreStagingPrefix) {
			t.Fatalf("staging directory %q was not cleaned up on successful restore", entry.Name())
		}
	}
}

func TestAutoSchedulerSerializesWithExclusiveWorkspaceAndDrainsOnClose(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "store")
	repoDir := filepath.Join(stateRoot, "versions", "repository")
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspacechange.ForgetWorkspace(workspace) })

	changes, err := workspacechange.ForWorkspaceAt(workspace, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(workspace, repoDir)
	service.autoVersionIdleDelay = 5 * time.Millisecond

	settings := VersionAutoSettings{
		TimedEnabled:         true,
		TimedIntervalMinutes: 1,
		Retention:            10,
	}

	enteredExclusive := make(chan struct{})
	releaseExclusive := make(chan struct{})
	exclusiveDone := make(chan error, 1)
	go func() {
		exclusiveDone <- changes.WithExclusiveWorkspace(context.Background(), func() error {
			if err := os.WriteFile(filepath.Join(workspace, "chapters", "part1.md"), []byte("one"), 0o644); err != nil {
				return err
			}
			close(enteredExclusive)
			<-releaseExclusive
			return os.WriteFile(filepath.Join(workspace, "chapters", "part2.md"), []byte("two"), 0o644)
		})
	}()

	<-enteredExclusive
	service.ScheduleAutoVersion(settings)
	// Give timer time to fire and block on WithConsistentWorkspaceSnapshot.
	time.Sleep(25 * time.Millisecond)

	close(releaseExclusive)
	if err := <-exclusiveDone; err != nil {
		t.Fatal(err)
	}

	// Wait briefly for the unblocked auto-version run to commit, then Close() must drain cleanly.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history, histErr := service.History(10)
		if histErr == nil && len(history) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	service.Close()

	history, err := service.History(10)
	if err != nil || len(history) != 1 {
		t.Fatalf("expected 1 consistent auto version, got history=%#v err=%v", history, err)
	}
	committed, err := service.commitFiles(history[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok1 := committed["chapters/part1.md"]; !ok1 {
		t.Fatalf("auto version missed part1.md: %#v", committed)
	}
	if _, ok2 := committed["chapters/part2.md"]; !ok2 {
		t.Fatalf("auto version captured torn snapshot missing part2.md: %#v", committed)
	}
}

func TestCloseWaitsForActiveScheduledAutoVersion(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "store")
	repoDir := filepath.Join(stateRoot, "versions", "repository")
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspacechange.ForgetWorkspace(workspace) })

	changes, err := workspacechange.ForWorkspaceAt(workspace, stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(workspace, repoDir)
	service.autoVersionIdleDelay = time.Millisecond

	enteredExclusive := make(chan struct{})
	releaseExclusive := make(chan struct{})
	go func() {
		_ = changes.WithExclusiveWorkspace(context.Background(), func() error {
			close(enteredExclusive)
			<-releaseExclusive
			return nil
		})
	}()
	<-enteredExclusive

	service.ScheduleAutoVersion(DefaultAutoSettings())
	// Wait until autoRunning becomes true.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		service.autoMu.Lock()
		running := service.autoRunning
		service.autoMu.Unlock()
		if running {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	var closedFlag atomic.Bool
	closeDone := make(chan struct{})
	go func() {
		service.Close()
		closedFlag.Store(true)
		close(closeDone)
	}()

	time.Sleep(20 * time.Millisecond)
	if closedFlag.Load() {
		t.Fatal("Close() returned while runScheduledAutoVersion was still blocked on workspace lock")
	}
	close(releaseExclusive)
	<-closeDone

	service.autoMu.Lock()
	stillRunning := service.autoRunning
	service.autoMu.Unlock()
	if stillRunning {
		t.Fatal("autoRunning still true after Close() returned")
	}
}

func TestMaybeCreateTimedDoesNotPolluteWorkspaceBeforeForWorkspaceAt(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	stateRoot := filepath.Join(root, "store")
	repoDir := filepath.Join(stateRoot, "versions", "repository")
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspacechange.ForgetWorkspace(workspace) })

	service := NewService(workspace, repoDir)
	t.Cleanup(service.Close)
	res, err := service.MaybeCreateTimed(DefaultAutoSettings())
	if err != nil || res.Version == nil {
		t.Fatalf("MaybeCreateTimed failed: res=%#v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".denova", "changes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MaybeCreateTimed created unexpected workspace-local .denova/changes: %v", err)
	}
	if _, err := workspacechange.ForWorkspaceAt(workspace, stateRoot); err != nil {
		t.Fatalf("ForWorkspaceAt failed after MaybeCreateTimed: %v", err)
	}
}
