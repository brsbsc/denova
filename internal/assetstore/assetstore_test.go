package assetstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"testing"
)

func TestSaveDiscardAndMergeMetadata(t *testing.T) {
	workspace := t.TempDir()
	ctx := context.Background()

	gameDir, err := GameDirectory("story-01")
	if err != nil {
		t.Fatalf("GameDirectory valid ID failed: %v", err)
	}
	if _, err := GameDirectory("../escape"); err == nil {
		t.Fatal("GameDirectory should reject traversal IDs")
	}
	if _, err := GameDirectory("CON"); err == nil {
		t.Fatal("GameDirectory should reject Windows device names")
	}

	assetA := NewPath(gameDir, "png")
	assetB := NewPath(gameDir, "png")
	if !IsManaged(assetA) || !IsRetained(assetA) || IsMetadata(assetA) {
		t.Fatalf("unexpected classification for %q", assetA)
	}
	if IsManaged(CoverPath) {
		t.Fatal("mutable CoverPath must not be classified as immutable managed asset")
	}

	if err := Save(ctx, workspace, File{
		Path:       assetA,
		Data:       []byte("png-bytes-a"),
		Generation: map[string]string{"prompt": "ancient tower"},
	}); err != nil {
		t.Fatalf("Save assetA failed: %v", err)
	}
	if err := Save(ctx, workspace, File{
		Path:       assetB,
		Data:       []byte("png-bytes-b"),
		Generation: map[string]string{"prompt": "misty forest"},
	}); err != nil {
		t.Fatalf("Save assetB failed: %v", err)
	}

	metaAbs := filepath.Join(workspace, filepath.FromSlash(MetaPath(assetA)))
	rawMeta, err := os.ReadFile(metaAbs)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMetadata(rawMeta)
	if err != nil || len(decoded.Files) != 2 {
		t.Fatalf("DecodeMetadata = %#v, err=%v", decoded, err)
	}

	if err := Discard(ctx, workspace, assetA); err != nil {
		t.Fatalf("Discard assetA failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(assetA))); !os.IsNotExist(err) {
		t.Fatalf("discarded file still exists: %v", err)
	}
	rawAfterDiscard, err := os.ReadFile(metaAbs)
	if err != nil {
		t.Fatal(err)
	}
	decodedAfter, err := DecodeMetadata(rawAfterDiscard)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := decodedAfter.Files[path.Base(assetA)]; exists {
		t.Fatalf("discarded asset metadata entry remained: %#v", decodedAfter.Files)
	}
	if _, exists := decodedAfter.Files[path.Base(assetB)]; !exists {
		t.Fatalf("sibling asset metadata was lost: %#v", decodedAfter.Files)
	}

	// MergeMetadata keeps target entries authoritative while filling retained newer entries.
	targetBytes, err := json.Marshal(Metadata{
		Version: 1,
		Files: map[string]json.RawMessage{
			path.Base(assetA): json.RawMessage(`{"prompt":"target-version"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	retainedBytes, err := json.Marshal(Metadata{
		Version: 1,
		Files: map[string]json.RawMessage{
			path.Base(assetA): json.RawMessage(`{"prompt":"retained-override-ignored"}`),
			path.Base(assetB): json.RawMessage(`{"prompt":"retained-new"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mergedRaw, err := MergeMetadata(targetBytes, retainedBytes)
	if err != nil {
		t.Fatalf("MergeMetadata failed: %v", err)
	}
	merged, err := DecodeMetadata(mergedRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(merged.Files[path.Base(assetA)], []byte("target-version")) {
		t.Fatalf("target entry was overwritten during merge: %s", merged.Files[path.Base(assetA)])
	}
	if !bytes.Contains(merged.Files[path.Base(assetB)], []byte("retained-new")) {
		t.Fatalf("retained entry was not preserved during merge: %s", merged.Files[path.Base(assetB)])
	}
}
