package versions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"denova/internal/assetstore"
	"denova/internal/book/lore"
	"denova/internal/revisionfile"
)

// A lore-only restore must also restore the files its target collection uses.
// Additional files remain immutable history and are never garbage-collected here.
func (s *Service) loreRestorePaths(id string, paths []string) ([]string, error) {
	if !slices.Contains(paths, lore.ItemsRelativePath) {
		return paths, nil
	}
	target, err := s.commitFiles(id)
	if err != nil {
		return nil, err
	}
	return s.loreRestorePathsWithTarget(id, paths, target)
}

func (s *Service) loreRestorePathsWithTarget(id string, paths []string, target map[string]versionFileData) ([]string, error) {
	if !slices.Contains(paths, lore.ItemsRelativePath) {
		return paths, nil
	}
	if _, ok := target[lore.ItemsRelativePath]; !ok {
		return paths, nil
	}
	data, err := s.readCommitFile(id, lore.ItemsRelativePath)
	if err != nil {
		return nil, err
	}
	dependencies, err := lore.MaterialFilePaths(data)
	if err != nil {
		return nil, err
	}
	result := slices.Clone(paths)
	for _, name := range dependencies {
		if _, ok := target[name]; !ok {
			return nil, fmt.Errorf("target version is missing lore material: %s", name)
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	return result, nil
}

// Git's forced checkout deletes tracked files absent from the target. Preserve
// newer media inside an excluded staging dir in workspace, then put it back even if checkout fails.
func (s *Service) preservingNewerLoreMedia(id string, restore func() error) error {
	target, err := s.commitFiles(id)
	if err != nil {
		return err
	}
	files, err := s.collectVisibleFiles()
	if err != nil {
		return err
	}
	return s.preservingNewerLoreMediaWithSnapshots(target, files, restore)
}

func (s *Service) preservingNewerLoreMediaWithSnapshots(target map[string]versionFileData, files []versionFileData, restore func() error) (result error) {
	retained := make([]versionFileData, 0)
	for _, file := range files {
		if !assetstore.IsRetained(file.Path) {
			continue
		}
		if _, ok := target[file.Path]; ok && !assetstore.IsMetadata(file.Path) {
			continue
		}
		retained = append(retained, file)
	}
	if len(retained) == 0 {
		return restore()
	}
	saved, err := os.MkdirTemp(s.workspace, versionRestoreStagingPrefix+"media-*")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(retained))
	for _, file := range retained {
		if err := copyRestoreMedia(file.Abs, filepath.Join(saved, filepath.FromSlash(file.Path))); err != nil {
			_ = os.RemoveAll(saved)
			return err
		}
		names = append(names, file.Path)
	}
	defer func() {
		var restoreErr error
		for _, name := range names {
			if assetstore.IsMetadata(name) {
				previous, readErr := os.ReadFile(filepath.Join(saved, filepath.FromSlash(name)))
				restoreErr = errors.Join(restoreErr, readErr)
				if readErr == nil {
					_, mergeErr := revisionfile.Mutate(context.Background(), filepath.Join(s.workspace, filepath.FromSlash(name)), revisionfile.Options{}, func(snapshot revisionfile.Snapshot) ([]byte, error) {
						return assetstore.MergeMetadata(snapshot.Content, previous)
					})
					restoreErr = errors.Join(restoreErr, mergeErr)
				}
			} else {
				restoreErr = errors.Join(restoreErr, copyRestoreMedia(filepath.Join(saved, filepath.FromSlash(name)), filepath.Join(s.workspace, filepath.FromSlash(name))))
			}
		}
		if restoreErr != nil {
			result = errors.Join(result, fmt.Errorf("restore retained media from %s: %w", saved, restoreErr))
			return
		}
		result = errors.Join(result, os.RemoveAll(saved))
	}()
	return restore()
}
func copyRestoreMedia(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	output, err := os.Create(target)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}
