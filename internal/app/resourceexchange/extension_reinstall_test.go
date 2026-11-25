package resourceexchange

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"denova/internal/platform"
)

func TestPlanExtensionReinstallsRemovedPackageAndAppliesUpdatedGrantsOnSameDigest(t *testing.T) {
	ctx := context.Background()
	s := testService(t)
	fixtureDir := t.TempDir()
	for _, layer := range []string{"runtime", "static"} {
		part, err := readFiles(filepath.Join("..", "..", "platform", "testdata", layer))
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range part {
			dest := filepath.Join(fixtureDir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// 1. Initial install with required grant only ("gameData").
	cand1, err := s.platform.PreviewExtensionDirectory(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	plan1, err := s.PlanExtension(ctx, cand1.ID, []string{"gameData"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, plan1.ID); err != nil {
		t.Fatal(err)
	}

	// 2. Mark extension Removed: true, then reinstall the exact same digest.
	if err := s.platform.SetAvailability(ctx, platform.Game, cand1.Manifest.ID, platform.PackageAvailability{Removed: true}); err != nil {
		t.Fatal(err)
	}
	cand2, err := s.platform.PreviewExtensionDirectory(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	plan2, err := s.PlanExtension(ctx, cand2.ID, []string{"gameData"})
	if err != nil {
		t.Fatalf("PlanExtension after removal failed: %v", err)
	}
	if _, err := s.Apply(ctx, plan2.ID); err != nil {
		t.Fatalf("Apply reinstall after removal failed (expected no ErrImportReviewRequired): %v", err)
	}
	games, err := s.platform.List(platform.Game)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].Removed || !games[0].Enabled || !slices.Equal(games[0].Grants, []string{"gameData"}) {
		t.Fatalf("unexpected game state after reinstall: %+v", games)
	}

	// 3. Reinstall same digest (not removed) with updated optional grants ("agents.run", "tools.invoke").
	cand3, err := s.platform.PreviewExtensionDirectory(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	wantGrants := []string{"agents.run", "gameData", "tools.invoke"}
	plan3, err := s.PlanExtension(ctx, cand3.ID, wantGrants)
	if err != nil {
		t.Fatalf("PlanExtension with updated grants failed: %v", err)
	}
	if _, err := s.Apply(ctx, plan3.ID); err != nil {
		t.Fatalf("Apply with updated grants failed: %v", err)
	}
	games, err = s.platform.List(platform.Game)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(games[0].Grants)
	if len(games) != 1 || games[0].Removed || !games[0].Enabled || !slices.Equal(games[0].Grants, wantGrants) {
		t.Fatalf("updated grants were not applied on same-digest reinstall: got %+v want %v", games[0], wantGrants)
	}
}
