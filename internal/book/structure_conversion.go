package book

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// StructureTitles supplies localized names only for containers absent in the
// original manuscript. Existing author-owned titles are always retained.
type StructureTitles struct{ Volume, Section string }

type StructurePreview struct {
	From           ChapterStructure `json:"from"`
	To             ChapterStructure `json:"to"`
	Revision       string           `json:"revision"`
	Before         []ManuscriptNode `json:"before"`
	After          []ManuscriptNode `json:"after"`
	MergedChapters int              `json:"merged_chapters"`
	FilesBefore    int              `json:"files_before"`
	FilesAfter     int              `json:"files_after"`
	files          map[string][]byte
	confirmed      map[string]bool
}

type manuscriptVolume struct {
	title    string
	chapters []manuscriptChapter
}
type manuscriptChapter struct {
	title     string
	parts     []manuscriptPart
	container bool
}
type manuscriptPart struct {
	title, content string
	confirmed      bool
}

// PreviewStructure reads a complete source snapshot. Its revision covers prose,
// empty directories, the selected preset and confirmation state, so application
// cannot accidentally use a preview of different content.
func (s *Service) PreviewStructure(target ChapterStructure, titles StructureTitles) (StructurePreview, error) {
	if !target.Valid() {
		return StructurePreview{}, ErrInvalidStructure
	}
	from, err := ReadChapterStructure(s.workspace)
	if err != nil {
		return StructurePreview{}, err
	}
	nodes, err := s.ManuscriptTree()
	if err != nil {
		return StructurePreview{}, err
	}
	status, err := s.readChapterStatusStore()
	if err != nil {
		return StructurePreview{}, err
	}
	hash := sha256.New()
	statusData, _ := json.Marshal(status)
	hash.Write(statusData)
	// Hash every entry, including hidden files, and reject material that cannot
	// be represented by a preset rather than dropping author data.
	err = filepath.WalkDir(filepath.Join(s.workspace, "chapters"), func(path string, entry os.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) && path == filepath.Join(s.workspace, "chapters") {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(s.workspace, path)
		rel = filepath.ToSlash(rel)
		fmt.Fprintf(hash, "%s\x00%t\x00", rel, entry.IsDir())
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrUnsupportedStructure, rel)
		}
		if entry.IsDir() {
			if strings.Count(rel, "/") > 2 || (strings.HasPrefix(entry.Name(), ".") && rel != "chapters") {
				return fmt.Errorf("%w: %s", ErrUnsupportedStructure, rel)
			}
			return nil
		}
		if !entry.Type().IsRegular() || (rel != structureFile && entry.Name() != ".keep" && !isChapterTextFile(entry.Name())) || (strings.HasPrefix(entry.Name(), ".") && rel != structureFile && entry.Name() != ".keep") {
			return fmt.Errorf("%w: %s", ErrUnsupportedStructure, rel)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%d\x00", len(data))
		hash.Write(data)
		return nil
	})
	if err != nil {
		return StructurePreview{}, err
	}
	volumes := []manuscriptVolume{}
	readChapter := func(node ManuscriptNode) (manuscriptChapter, error) {
		chapter := manuscriptChapter{title: node.Title, container: node.Directory}
		parts := []ManuscriptNode{node}
		if node.Directory {
			parts = node.Children
		}
		for _, part := range parts {
			if part.Directory {
				return chapter, fmt.Errorf("%w: %s", ErrUnsupportedStructure, part.Path)
			}
			body, err := s.ReadFile(part.Path)
			if err != nil {
				return chapter, err
			}
			chapter.parts = append(chapter.parts, manuscriptPart{title: part.Title, content: body, confirmed: status.Chapters[part.Path]})
		}
		return chapter, nil
	}
	for _, node := range nodes {
		if node.Directory {
			volume := manuscriptVolume{title: node.Title}
			for _, child := range node.Children {
				chapter, err := readChapter(child)
				if err != nil {
					return StructurePreview{}, err
				}
				volume.chapters = append(volume.chapters, chapter)
			}
			volumes = append(volumes, volume)
		} else {
			chapter, err := readChapter(node)
			if err != nil {
				return StructurePreview{}, err
			}
			if len(volumes) == 0 || volumes[len(volumes)-1].title != "" {
				volumes = append(volumes, manuscriptVolume{})
			}
			volumes[len(volumes)-1].chapters = append(volumes[len(volumes)-1].chapters, chapter)
		}
	}
	plan := buildStructure(volumes, target, titles)
	plan.From, plan.Before, plan.Revision = from, nodes, hex.EncodeToString(hash.Sum(nil))
	return plan, nil
}

func buildStructure(volumes []manuscriptVolume, target ChapterStructure, titles StructureTitles) StructurePreview {
	plan := StructurePreview{To: target, After: []ManuscriptNode{}, files: map[string][]byte{}, confirmed: map[string]bool{}}
	chapterOrder := 0
	for vi, volume := range volumes {
		parent := "chapters"
		volumeTitle := volume.title
		if volumeTitle == "" {
			volumeTitle = titles.Volume
		}
		volumeNode := ManuscriptNode{}
		if target != StructureChapters {
			parent = prefixedVolumePath(vi+1, volumeTitle)
			volumeNode = ManuscriptNode{Path: parent, Title: volumeTitle, Directory: true}
			plan.files[parent+"/.keep"] = []byte{}
		} else if len(volume.chapters) == 0 {
			// Preserve the title of an empty volume as an empty chapter.
			volume.chapters = []manuscriptChapter{{title: volumeTitle}}
		}
		for _, chapter := range volume.chapters {
			chapterOrder++
			chapterPath := parent + "/" + chapterFilename(chapterOrder, chapter.title, "")
			plan.FilesBefore += len(chapter.parts)
			node := ManuscriptNode{Path: chapterPath, Title: chapter.title}
			if target == StructureSections {
				node.Path = strings.TrimSuffix(chapterPath, ".md")
				node.Directory = true
				plan.files[node.Path+"/.keep"] = []byte{}
				for pi, part := range chapter.parts {
					title := part.title
					if !chapter.container {
						title = titles.Section
					}
					path := node.Path + "/" + sectionFilename(pi+1, title)
					node.Children = append(node.Children, ManuscriptNode{Path: path, Title: title})
					plan.files[path] = []byte(part.content)
					if part.confirmed {
						plan.confirmed[path] = true
					}
					plan.FilesAfter++
				}
			} else {
				content := ""
				confirmed := len(chapter.parts) > 0
				if chapter.container {
					plan.MergedChapters++
					if len(chapter.parts) > 0 {
						content = "# " + chapter.title + "\n\n"
					}
					for _, part := range chapter.parts {
						content += withManuscriptHeading(part.title, part.content) + "\n\n"
					}
				} else if len(chapter.parts) > 0 {
					content = chapter.parts[0].content
				}
				for _, part := range chapter.parts {
					confirmed = confirmed && part.confirmed
				}
				plan.files[node.Path] = []byte(content)
				if confirmed {
					plan.confirmed[node.Path] = true
				}
				plan.FilesAfter++
			}
			if target == StructureChapters {
				plan.After = append(plan.After, node)
			} else {
				volumeNode.Children = append(volumeNode.Children, node)
			}
		}
		if target != StructureChapters {
			plan.After = append(plan.After, volumeNode)
		}
	}
	return plan
}

func sectionFilename(order int, title string) string {
	name := safeFilenamePart(title)
	if name == "" {
		name = "section"
	}
	return fmt.Sprintf("s%05d-%s.md", order, name)
}

func withManuscriptHeading(title, content string) string {
	trimmed := strings.TrimSpace(content)
	line, _, _ := strings.Cut(trimmed, "\n")
	if sameExportTitle(line, title) {
		return content
	}
	return "## " + title + "\n\n" + content
}

// PreserveEmptyManuscriptDirectories makes released, filesystem-created empty
// containers visible to file-based version snapshots before a conversion.
// The caller must hold the workspace mutation lease and validate the preview.
func (s *Service) PreserveEmptyManuscriptDirectories(nodes []ManuscriptNode) error {
	for _, node := range nodes {
		if !node.Directory {
			continue
		}
		if len(node.Children) > 0 {
			if err := s.PreserveEmptyManuscriptDirectories(node.Children); err != nil {
				return err
			}
			continue
		}
		marker := filepath.Join(s.workspace, filepath.FromSlash(node.Path), ".keep")
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ApplyStructure requires the caller's exclusive workspace lease and a durable
// version backup. Mutate chapters in place rather than renaming the watched
// chapters root directory (which Windows rejects with ERROR_ACCESS_DENIED when
// fsnotify holds ReadDirectoryChangesW handles on chapters or child containers)
// and roll back both manuscript files and confirmation state if any step fails.
func (s *Service) ApplyStructure(plan StructurePreview) (err error) {
	if !plan.To.Valid() || plan.files == nil {
		return ErrInvalidStructure
	}
	structureData, err := json.Marshal(struct {
		Structure ChapterStructure `json:"structure"`
	}{plan.To})
	if err != nil {
		return err
	}
	targetFiles := make(map[string][]byte, len(plan.files)+1)
	for rel, data := range plan.files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
		targetFiles[clean] = data
	}
	targetFiles[structureFile] = append(structureData, '\n')
	targetDirs := manuscriptAncestorDirs(targetFiles)

	oldStatus, err := s.readChapterStatusStore()
	if err != nil {
		return err
	}
	root := filepath.Join(s.workspace, "chapters")
	if err = mkdirAllRetryPermission(root); err != nil {
		return err
	}
	beforeDirs, beforeFiles, err := snapshotManuscriptSubtree(s.workspace, root)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			restoreErr := syncManuscriptSubtree(s.workspace, beforeDirs, beforeFiles)
			statusErr := s.writeChapterStatusStore(oldStatus)
			err = errors.Join(err, restoreErr, statusErr)
		}
		s.InvalidateSummary(nil, true)
	}()
	if err = syncManuscriptSubtree(s.workspace, targetDirs, targetFiles); err != nil {
		return err
	}
	return s.writeChapterStatusStore(chapterStatusStore{Chapters: plan.confirmed})
}

func manuscriptAncestorDirs(files map[string][]byte) map[string]bool {
	dirs := map[string]bool{"chapters": true}
	for rel := range files {
		for dir := path.Dir(rel); dir != "." && dir != "/" && dir != ""; dir = path.Dir(dir) {
			dirs[dir] = true
			if dir == "chapters" {
				break
			}
		}
	}
	return dirs
}

func snapshotManuscriptSubtree(workspace, root string) (map[string]bool, map[string][]byte, error) {
	dirs := map[string]bool{"chapters": true}
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(fullPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(workspace, fullPath)
		if relErr != nil {
			return relErr
		}
		norm := filepath.ToSlash(rel)
		if entry.IsDir() {
			dirs[norm] = true
			return nil
		}
		data, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			return readErr
		}
		files[norm] = data
		return nil
	})
	return dirs, files, err
}

func syncManuscriptSubtree(workspace string, targetDirs map[string]bool, targetFiles map[string][]byte) error {
	root := filepath.Join(workspace, "chapters")
	currentDirs, currentFiles, err := snapshotManuscriptSubtree(workspace, root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for rel := range currentFiles {
		if _, keep := targetFiles[rel]; !keep || targetDirs[rel] {
			if removeErr := os.Remove(filepath.Join(workspace, filepath.FromSlash(rel))); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return removeErr
			}
		}
	}
	if err := removeObsoleteManuscriptDirs(workspace, currentDirs, targetDirs); err != nil {
		return err
	}
	sortedDirs := make([]string, 0, len(targetDirs))
	for rel := range targetDirs {
		sortedDirs = append(sortedDirs, rel)
	}
	sort.Strings(sortedDirs)
	for _, rel := range sortedDirs {
		if err := mkdirAllRetryPermission(filepath.Join(workspace, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	sortedFiles := make([]string, 0, len(targetFiles))
	for rel := range targetFiles {
		sortedFiles = append(sortedFiles, rel)
	}
	sort.Strings(sortedFiles)
	for _, rel := range sortedFiles {
		if err := atomicWriteManuscriptFile(workspace, rel, targetFiles[rel]); err != nil {
			return err
		}
	}
	return removeObsoleteManuscriptDirs(workspace, currentDirs, targetDirs)
}

func removeObsoleteManuscriptDirs(workspace string, currentDirs, targetDirs map[string]bool) error {
	obsolete := make([]string, 0, len(currentDirs))
	for rel := range currentDirs {
		if rel == "chapters" || targetDirs[rel] {
			continue
		}
		obsolete = append(obsolete, rel)
	}
	sort.SliceStable(obsolete, func(i, j int) bool { return len(obsolete[i]) > len(obsolete[j]) })
	for _, rel := range obsolete {
		fullPath := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				continue
			}
			return err
		}
	}
	return nil
}

func mkdirAllRetryPermission(dirPath string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		if err = os.MkdirAll(dirPath, 0o755); err == nil || !errors.Is(err, os.ErrPermission) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

func atomicWriteManuscriptFile(workspace, rel string, data []byte) error {
	targetPath := filepath.Join(workspace, filepath.FromSlash(rel))
	parentPath := filepath.Dir(targetPath)
	if err := mkdirAllRetryPermission(parentPath); err != nil {
		return err
	}
	var random [8]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		return err
	}
	tempPath := filepath.Join(parentPath, fmt.Sprintf(".%s.denova-%x.tmp", filepath.Base(targetPath), random[:]))
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return err
	}
	removeTemp = false
	return nil
}
