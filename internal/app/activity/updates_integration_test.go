package activity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"denova/internal/buildinfo"
	"denova/internal/update"
)

func TestUpgradeNotesReadFullInstalledHistoryInRequestLanguage(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "0.6.15"
	t.Cleanup(func() { buildinfo.Version = previous })
	root := t.TempDir()
	path := filepath.Join(root, "CHANGELOG.md")
	content := "## [Unreleased]\n- Not published\n"
	for version := 0; version <= 15; version++ {
		content += fmt.Sprintf("\n## [v0.6.%d] - 2026-10-20\n### Brief / 简要说明\n#### 中文\n- 中文变化 %d\n#### English\n- English change %d\n### Added / 新增\n- 中文详情 %d\n- English detail %d\n", version, version, version, version, version)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DENOVA_CHANGELOG_PATH", path)
	service := &Service{dataDir: root, updates: update.NewService()}
	for _, locale := range []string{"zh-CN", "en-US"} {
		notes, err := service.UpdateNotes(context.Background(), "v0.6.0", "v0.6.15", locale)
		if err != nil || !notes.Complete || len(notes.Releases) != 15 {
			t.Fatalf("range was truncated to the notification feed: %+v, %v", notes, err)
		}
		for index, release := range notes.Releases {
			if release.Version != fmt.Sprintf("v0.6.%d", 15-index) {
				t.Fatalf("incorrect numeric version order: %+v", notes.Releases)
			}
			if locale == "zh-CN" && (strings.Contains(release.Brief+release.Body, "English") || strings.Contains(release.Body, "Added") || !strings.Contains(release.Brief, "中文变化") || !strings.Contains(release.Body, "中文详情")) {
				t.Fatalf("incorrect Chinese release: %+v", release)
			}
			if locale == "en-US" && (strings.Contains(release.Brief+release.Body, "中文") || strings.Contains(release.Body, "新增") || !strings.Contains(release.Brief, "English change") || !strings.Contains(release.Body, "English detail")) {
				t.Fatalf("incorrect English release: %+v", release)
			}
			if strings.Contains(release.Body, "change") || strings.Contains(release.Body, "变化") {
				t.Fatalf("brief was duplicated in details: %+v", release)
			}
		}
	}
	history, err := service.UpdateNotes(context.Background(), "", "0.6.15", "en-US")
	if err != nil || !history.Complete || len(history.Releases) != 16 {
		t.Fatalf("published history = %+v, %v", history, err)
	}
	partial, err := service.UpdateNotes(context.Background(), "0.5.1", "0.6.15", "en-US")
	if err != nil || partial.Complete {
		t.Fatalf("missing baseline was reported as a complete range: %+v, %v", partial, err)
	}
	if _, err := service.UpdateNotes(context.Background(), "0.6.15", "0.6.15", "en-US"); !errors.Is(err, ErrUpdateNotesRange) {
		t.Fatalf("accepted an empty upgrade range: %v", err)
	}
}

func TestUpgradeNotesSupportForkHotfixVersions(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "0.8.0"
	t.Cleanup(func() { buildinfo.Version = previous })

	root := t.TempDir()
	path := filepath.Join(root, "CHANGELOG.md")
	entry := func(version, label string) string {
		return fmt.Sprintf("\n## [%s] - 2026-11-22\n### Brief / 简要说明\n#### 中文\n- 中文变化 %s\n#### English\n- English change %s\n", version, label, label)
	}
	content := "## [Unreleased]\n- Not published\n" +
		entry("v0.8.0", "0.8.0") +
		entry("v0.8.0-rc.1", "rc") +
		entry("v0.6.3", "0.6.3") +
		entry("v0.6.2", "0.6.2") +
		entry("v0.5.1fix19", "fix19") +
		entry("v0.5.1fix18", "fix18") +
		entry("v0.5.1", "0.5.1")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DENOVA_CHANGELOG_PATH", path)
	service := &Service{dataDir: root, updates: update.NewService()}

	notes, err := service.UpdateNotes(context.Background(), "0.5.1fix18", "0.8.0", "zh-CN")
	if err != nil {
		t.Fatalf("UpdateNotes(0.5.1fix18 -> 0.8.0) error: %v", err)
	}
	if !notes.Complete {
		t.Fatalf("expected complete hotfix range, got %+v", notes)
	}
	wantVersions := []string{"v0.8.0", "v0.6.3", "v0.6.2", "v0.5.1fix19"}
	if len(notes.Releases) != len(wantVersions) {
		t.Fatalf("releases = %+v, want %v", notes.Releases, wantVersions)
	}
	for i, want := range wantVersions {
		if notes.Releases[i].Version != want {
			t.Fatalf("release[%d] = %q, want %q (all: %+v)", i, notes.Releases[i].Version, want, notes.Releases)
		}
	}

	// Real-world fork upgrades often move from a hotfix whose changelog entry is not
	// listed separately; the range still succeeds and marks Complete=false.
	withoutHotfixBaseline := "## [Unreleased]\n- Not published\n" +
		entry("v0.8.0", "0.8.0") +
		entry("v0.6.3", "0.6.3") +
		entry("v0.5.1", "0.5.1")
	if err := os.WriteFile(path, []byte(withoutHotfixBaseline), 0o600); err != nil {
		t.Fatal(err)
	}
	partial, err := service.UpdateNotes(context.Background(), "0.5.1fix18", "0.8.0", "zh-CN")
	if err != nil {
		t.Fatalf("UpdateNotes without hotfix baseline error: %v", err)
	}
	if partial.Complete {
		t.Fatalf("expected incomplete range when baseline hotfix is absent: %+v", partial)
	}
	if len(partial.Releases) != 2 || partial.Releases[0].Version != "v0.8.0" || partial.Releases[1].Version != "v0.6.3" {
		t.Fatalf("partial releases = %+v, want [v0.8.0 v0.6.3]", partial.Releases)
	}

	for _, invalid := range []struct {
		from, to string
	}{
		{"0.8.0", "0.5.1fix18"},
		{"0.5.1fix19", "0.5.1fix18"},
		{"0.5.1fix18", "0.5.1fix18"},
		{"0.5.1fix18", "0.8.0-rc.1"},
		{"0.5.1-rc.1", "0.8.0"},
		{"dev", "0.8.0"},
		{"0.5.1fix0", "0.8.0"},
		{"0.5.1fix18", "0.8"},
	} {
		if _, err := service.UpdateNotes(context.Background(), invalid.from, invalid.to, "zh-CN"); !errors.Is(err, ErrUpdateNotesRange) {
			t.Fatalf("UpdateNotes(%q, %q) err = %v, want ErrUpdateNotesRange", invalid.from, invalid.to, err)
		}
	}
}
