package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"denova/internal/portablepath"
	"golang.org/x/text/unicode/norm"
)

func TestNovelImportNormalizesUnicodeAndStripsControlChars(t *testing.T) {
	if got := safeFilenamePart(norm.NFD.String("Cafe\u0301\x00\x07\x1fPart")); got != "Café-Part" {
		t.Fatalf("safeFilenamePart NFD+control = %q, want %q", got, "Café-Part")
	}

	workspace := t.TempDir()
	nfdTitle := norm.NFD.String("Cafe\u0301\x07Dawn")
	raw := []byte("Chapter 1 " + nfdTitle + "\nFirst body.\n\nChapter 2 Second\t\x1fPart\nSecond body.\n")

	preview, paths, err := ImportNovelToWorkspace(workspace, norm.NFD.String("Booke\u0301.txt"), raw)
	if err != nil {
		t.Fatalf("ImportNovelToWorkspace failed: %v", err)
	}
	if norm.NFC.String(preview.Title) != preview.Title {
		t.Fatalf("preview title is not NFC normalized: %q", preview.Title)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 imported chapters, got %v", paths)
	}
	for _, rel := range paths {
		if err := portablepath.Validate(rel); err != nil {
			t.Fatalf("imported path %q is not portable: %v", rel, err)
		}
		if strings.ContainsAny(rel, "\x00\x07\x1f") {
			t.Fatalf("control character leaked into path %q", rel)
		}
	}
}

func TestImportNovelToWorkspaceRollsBackOnMidImportFailure(t *testing.T) {
	workspace := t.TempDir()
	raw := []byte("第一章 开端\n第一段正文。\n\n第二章 阻碍\n第二段正文。\n")
	preview, err := PreviewNovelImport("novel.txt", raw)
	if err != nil || len(preview.Chapters) != 2 {
		t.Fatalf("PreviewNovelImport failed: preview=%#v err=%v", preview, err)
	}

	// Place a directory at the exact path of chapter 2 so chapter 1 succeeds and chapter 2 fails.
	blocker := filepath.Join(workspace, filepath.FromSlash(preview.Chapters[1].Path))
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	keepMarker := filepath.Join(blocker, "keep.txt")
	if err := os.WriteFile(keepMarker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ImportNovelToWorkspace(workspace, "novel.txt", raw); err == nil {
		t.Fatal("expected ImportNovelToWorkspace to fail on directory obstacle")
	}

	firstChapter := filepath.Join(workspace, filepath.FromSlash(preview.Chapters[0].Path))
	if _, err := os.Stat(firstChapter); !os.IsNotExist(err) {
		t.Fatalf("chapter 1 was not rolled back on mid-import failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, structureFile)); !os.IsNotExist(err) {
		t.Fatalf("structure file was not rolled back on mid-import failure: %v", err)
	}
	if data, err := os.ReadFile(keepMarker); err != nil || string(data) != "keep" {
		t.Fatalf("pre-existing obstacle content should remain untouched: data=%q err=%v", data, err)
	}
}
