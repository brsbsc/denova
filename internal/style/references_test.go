package style

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"denova/internal/portablepath"
)

func TestSlugAndStoragePathsRejectDotfileAndWindowsReservedNames(t *testing.T) {
	for _, name := range []string{"", "!!!", "😊🎉", "---", "CON", "aux", "LPT1", "nul"} {
		if got := slugFilename(name); got != "" {
			t.Fatalf("slugFilename(%q) = %q, want empty", name, got)
		}
	}
	for _, raw := range []string{"", ".", "..", ".md", ".hidden.md", "CON", "CON.md", "AUX.txt", "LPT9.markdown"} {
		if got := StoragePath(raw); got != "" {
			t.Fatalf("StoragePath(%q) = %q, want empty", raw, got)
		}
		if got := NormalizeStoragePath(raw); got != "" {
			t.Fatalf("NormalizeStoragePath(%q) = %q, want empty", raw, got)
		}
	}
}

func TestLibraryCreateReadUpdateDeletePunctuationAndDeviceNames(t *testing.T) {
	lib := NewLibrary(t.TempDir())

	cases := []WriteRequest{
		{Name: "!!!", Description: "标点标题", Content: "短句紧绷。"},
		{Name: "😊", Description: "表情标题", Content: "轻快跳脱。"},
		{Name: "CON", Filename: "CON", Description: "保留设备名", Content: "克制冷静。"},
		{Name: strings.Repeat("长标题文风", 30), Content: "超长标题截断。"},
	}

	for _, tc := range cases {
		ref, err := lib.Create(tc)
		if err != nil {
			t.Fatalf("Create(%#v) failed: %v", tc, err)
		}
		base := filepath.Base(ref.Path)
		if err := portablepath.ValidateComponent(base); err != nil {
			t.Fatalf("created style filename %q is not portable: %v", base, err)
		}
		if strings.HasPrefix(base, ".") {
			t.Fatalf("created hidden dotfile %q", base)
		}

		doc, err := lib.Read(ref.DisplayPath)
		if err != nil {
			t.Fatalf("Read(%q) failed: %v", ref.DisplayPath, err)
		}
		updated, err := lib.Update(UpdateRequest{
			Path:         ref.DisplayPath,
			Content:      doc.Content + "\n补充段落。\n",
			BaseRevision: doc.Revision,
		})
		if err != nil {
			t.Fatalf("Update(%q) failed: %v", ref.DisplayPath, err)
		}
		if _, err := lib.Update(UpdateRequest{
			Path:         ref.DisplayPath,
			Content:      "stale",
			BaseRevision: doc.Revision,
		}); !errors.Is(err, ErrReferenceRevisionConflict) {
			t.Fatalf("expected ErrReferenceRevisionConflict, got %v", err)
		}
		if updated.Revision == doc.Revision {
			t.Fatal("expected updated revision to advance")
		}
		if err := lib.Delete(ref.DisplayPath); err != nil {
			t.Fatalf("Delete(%q) failed: %v", ref.DisplayPath, err)
		}
	}

	list, err := lib.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty library after deleting all items, got %#v", list)
	}
}
