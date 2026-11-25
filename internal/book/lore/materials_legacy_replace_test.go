package lore

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func encodeTestPNGBytes(t *testing.T, fill color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.SetRGBA(0, 0, fill)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSaveMaterialReplacesLegacyLoreImageAndCover(t *testing.T) {
	workspace := t.TempDir()
	legacyRel := "assets/lore/images/legacy-hero.png"
	legacyAbs := filepath.Join(workspace, filepath.FromSlash(legacyRel))
	if err := os.MkdirAll(filepath.Dir(legacyAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAbs, encodeTestPNGBytes(t, color.RGBA{R: 10, G: 20, B: 30, A: 255}), 0o644); err != nil {
		t.Fatal(err)
	}

	collection := Collection{
		Version:    loreItemsVersion,
		Categories: DefaultCategories(),
		Items: []Item{{
			ID:         "hero",
			Enabled:    true,
			Type:       "character",
			Name:       "林川",
			Importance: "major",
			LoadMode:   LoadModeAuto,
			Content:    "旧版图片条目",
			Image: &Image{
				ImagePath: legacyRel,
				AltText:   "旧立绘",
				MIMEType:  "image/png",
			},
		}},
	}
	raw, err := json.MarshalIndent(collection, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	itemsAbs := ItemsPath(workspace)
	if err := os.MkdirAll(filepath.Dir(itemsAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(itemsAbs, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	store := NewStore(workspace)
	before, err := store.Read("hero")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.ResolvedMaterials) != 1 || !strings.HasPrefix(before.ResolvedMaterials[0].ID, "legacy:hero:") {
		t.Fatalf("expected legacy material projection, got %#v", before.ResolvedMaterials)
	}

	updated, err := store.SaveMaterial(context.Background(), "hero", MaterialFile{
		Filename:       "new-hero.png",
		Data:           encodeTestPNGBytes(t, color.RGBA{R: 200, G: 100, B: 50, A: 255}),
		Source:         AssetSource{Kind: "upload"},
		Entry:          MaterialEntry{Name: "新立绘"},
		ReplaceAssetID: before.ResolvedMaterials[0].ID,
	})
	if err != nil {
		t.Fatalf("SaveMaterial replacing legacy image failed: %v", err)
	}
	if updated.Materials == nil || len(updated.ResolvedMaterials) != 1 {
		t.Fatalf("expected 1 replaced material, got %#v", updated)
	}
	if strings.HasPrefix(updated.ResolvedMaterials[0].ID, "legacy:") {
		t.Fatalf("expected promoted asset ID, got %q", updated.ResolvedMaterials[0].ID)
	}
	if updated.Materials.CoverAssetID != updated.ResolvedMaterials[0].ID {
		t.Fatalf("expected CoverAssetID to track replacement %q, got %q", updated.ResolvedMaterials[0].ID, updated.Materials.CoverAssetID)
	}
	if updated.Image == nil || updated.Image.ImagePath == legacyRel {
		t.Fatalf("expected resolved cover image to point to new asset, got %#v", updated.Image)
	}
}
