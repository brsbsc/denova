package book

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSafePath(t *testing.T) {
	workspace := t.TempDir()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "普通相对路径", path: "chapters/ch01.md"},
		{name: "拒绝绝对路径", path: filepath.Join(workspace, "chapters/ch01.md"), wantErr: true},
		{name: "拒绝越界路径", path: "../outside.md", wantErr: true},
		{name: "拒绝隐藏目录", path: ".nova/session.jsonl", wantErr: true},
		{name: "拒绝隐藏文件", path: "chapters/.secret", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := SafePath(workspace, tt.path)
			if tt.wantErr && err == nil {
				t.Fatalf("期望返回错误")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("不期望返回错误: %v", err)
			}
		})
	}
}

func TestServiceRename(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/ch01.md", "file", "hello"); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	newPath, err := service.Rename("chapters/ch01.md", "ch01-new.md")
	if err != nil {
		t.Fatalf("重命名失败: %v", err)
	}
	if newPath != "chapters/ch01-new.md" {
		t.Fatalf("新路径不符合预期: %s", newPath)
	}

	if _, err := service.Rename("chapters/ch01-new.md", "nested/name.md"); err == nil {
		t.Fatalf("包含路径分隔符的新名称应被拒绝")
	}
}

func TestServiceRenameRejectsPortableCollision(t *testing.T) {
	service := NewService(t.TempDir())
	for name, content := range map[string]string{"chapters/draft.md": "draft", "chapters/final.md": "final"} {
		if err := service.Create(name, "file", content); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Rename("chapters/draft.md", "FINAL.md"); err == nil || !strings.Contains(err.Error(), "portable filename collision") {
		t.Fatalf("rename must reject case-folded destination collisions: %v", err)
	}
	for name, content := range map[string]string{"chapters/draft.md": "draft", "chapters/final.md": "final"} {
		got, err := service.ReadFile(name)
		if err != nil || got != content {
			t.Fatalf("rename changed %s: content=%q, error=%v", name, got, err)
		}
	}
}

func TestServiceWriteFileIfRevisionRejectsStaleRevision(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/ch01.md", "file", "前端旧内容"); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	revision, err := service.FileRevision("chapters/ch01.md")
	if err != nil {
		t.Fatalf("读取文件版本失败: %v", err)
	}

	if !strings.HasPrefix(revision, "sha256:") {
		t.Fatalf("revision should be content-addressed, got %q", revision)
	}
	if err := service.WriteFile("chapters/ch01.md", "Agent 已更新的新内容"); err != nil {
		t.Fatalf("Agent 写入失败: %v", err)
	}

	if _, err := service.WriteFileIfRevision("chapters/ch01.md", "前端旧内容", revision); !errors.Is(err, ErrFileRevisionConflict) {
		t.Fatalf("期望旧版本写入被拒绝，实际: %v", err)
	}
	got, err := service.ReadFile("chapters/ch01.md")
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}
	if got != "Agent 已更新的新内容" {
		t.Fatalf("冲突后应保留 Agent 内容，实际: %q", got)
	}
}

func TestBuildFileTreeSkipsHiddenFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".nova"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".hidden"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "chapters", "ch01.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	tree, err := BuildFileTree(workspace)
	if err != nil {
		t.Fatalf("构建文件树失败: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "chapters" {
		t.Fatalf("文件树应只包含 chapters，实际: %#v", tree)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].Name != "ch01.md" {
		t.Fatalf("chapters 子节点不符合预期: %#v", tree[0].Children)
	}
}

func TestBuildFileTreeSortsDirsBeforeFiles(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "z_dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "a_dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "b.md"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	tree, err := BuildFileTree(workspace)
	if err != nil {
		t.Fatalf("构建文件树失败: %v", err)
	}
	got := []string{tree[0].Name, tree[1].Name, tree[2].Name, tree[3].Name}
	want := []string{"a_dir", "z_dir", "a.md", "b.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("排序不符合预期: want=%v got=%v", want, got)
	}
}

func TestBuildFileTreeSortsChineseChapterOrdinals(t *testing.T) {
	workspace := t.TempDir()
	chapterDir := filepath.Join(workspace, "chapters")
	if err := os.MkdirAll(chapterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"第十一章-潮声.md",
		"第一百一十一章-归途.md",
		"第一章-开局.md",
		"第一千一百一十一章-终局.md",
		"序章.md",
		"第十章-交锋.md",
	} {
		if err := os.WriteFile(filepath.Join(chapterDir, name), []byte("正文"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := BuildFileTree(workspace)
	if err != nil {
		t.Fatalf("构建文件树失败: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "chapters" {
		t.Fatalf("文件树根节点不符合预期: %#v", tree)
	}
	got := make([]string, 0, len(tree[0].Children))
	for _, node := range tree[0].Children {
		got = append(got, node.Name)
	}
	want := []string{
		"序章.md",
		"第一章-开局.md",
		"第十章-交锋.md",
		"第十一章-潮声.md",
		"第一百一十一章-归途.md",
		"第一千一百一十一章-终局.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("章节文件排序不符合预期: want=%v got=%v", want, got)
	}
}

func TestBuildFileTreeSortsChineseVolumeOrdinals(t *testing.T) {
	workspace := t.TempDir()
	chapterDir := filepath.Join(workspace, "chapters")
	for _, name := range []string{"第十一卷-潮声", "第一百一十一卷-归途", "第一卷-开局", "第十卷-交锋"} {
		if err := os.MkdirAll(filepath.Join(chapterDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	tree, err := BuildFileTree(workspace)
	if err != nil {
		t.Fatalf("构建文件树失败: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "chapters" {
		t.Fatalf("文件树根节点不符合预期: %#v", tree)
	}
	got := make([]string, 0, len(tree[0].Children))
	for _, node := range tree[0].Children {
		got = append(got, node.Name)
	}
	want := []string{"第一卷-开局", "第十卷-交锋", "第十一卷-潮声", "第一百一十一卷-归途"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("分卷目录排序不符合预期: want=%v got=%v", want, got)
	}
}

func TestServiceCreateExisting(t *testing.T) {
	service := NewService(t.TempDir())
	if err := service.Create("chapters/ch01.md", "file", "hello"); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}
	if err := service.Create("chapters/ch01.md", "file", "hello"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("重复创建应返回 os.ErrExist，实际: %v", err)
	}
}

func TestServiceDeleteRemovesFile(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/ch01.md", "file", "hello"); err != nil {
		t.Fatalf("创建文件失败: %v", err)
	}

	if err := service.Delete("chapters/ch01.md"); err != nil {
		t.Fatalf("删除文件失败: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "chapters", "ch01.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除后原路径应不存在，实际错误: %v", err)
	}
}

func TestServiceDeleteRemovesDirectory(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/volume/ch01.md", "file", "hello"); err != nil {
		t.Fatalf("创建目录文件失败: %v", err)
	}

	if err := service.Delete("chapters/volume"); err != nil {
		t.Fatalf("删除目录失败: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "chapters", "volume")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除后目录应不存在，实际错误: %v", err)
	}
}

func TestServiceRejectsSymlinkLeavesOnReadWriteCreateCopy(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	outsideExisting := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsideExisting, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDangling := filepath.Join(outsideDir, "escaped.md")

	if err := os.MkdirAll(filepath.Join(workspace, "chapters"), 0o755); err != nil {
		t.Fatal(err)
	}
	existingLink := filepath.Join(workspace, "chapters", "link.md")
	if err := os.Symlink(outsideExisting, existingLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	danglingLink := filepath.Join(workspace, "chapters", "dangling.md")
	if err := os.Symlink(outsideDangling, danglingLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	service := NewService(workspace)
	if _, err := service.ReadFile("chapters/link.md"); err == nil {
		t.Fatal("ReadFile should reject symlink leaf")
	}
	if err := service.WriteFile("chapters/dangling.md", "escaped"); err == nil {
		t.Fatal("WriteFile should reject dangling symlink leaf")
	}
	if err := service.WriteBinaryFile("chapters/dangling.md", []byte("escaped")); err == nil {
		t.Fatal("WriteBinaryFile should reject dangling symlink leaf")
	}
	if err := service.Create("chapters/dangling.md", "file", "escaped"); err == nil {
		t.Fatal("Create should reject dangling symlink leaf")
	}
	if err := service.Copy("chapters/link.md", "chapters/copied.md"); err == nil {
		t.Fatal("Copy should reject symlink source")
	}
	if _, err := os.Stat(outsideDangling); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dangling symlink target was created outside workspace: %v", err)
	}
	if got, err := os.ReadFile(outsideExisting); err != nil || string(got) != "secret" {
		t.Fatalf("external symlink target was modified: got=%q err=%v", got, err)
	}
}

func TestServiceCopyAndMoveRejectSelfOrDescendantTargets(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/ch01.md", "file", "chapter 1"); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"chapters", "chapters/backup", "CHAPTERS/nested/backup"} {
		if err := service.Copy("chapters", target); err == nil {
			t.Fatalf("Copy(chapters, %q) should fail", target)
		}
		if err := service.Move("chapters", target); err == nil {
			t.Fatalf("Move(chapters, %q) should fail", target)
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, "chapters", "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected Move created nested directories inside source: %v", err)
	}
	if err := service.Move("chapters/missing.md", "archive/sub/missing.md"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Move missing source = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "archive")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Move missing source should not create destination parent dirs: %v", err)
	}
}

func TestServiceWriteFileIfRevisionConcurrentCAS(t *testing.T) {
	workspace := t.TempDir()
	service := NewService(workspace)
	if err := service.Create("chapters/ch01.md", "file", "base"); err != nil {
		t.Fatal(err)
	}
	baseRev, err := service.FileRevision("chapters/ch01.md")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 8
	var wg sync.WaitGroup
	successes := 0
	conflicts := 0
	var mu sync.Mutex
	for i := 0; i < writers; i++ {
		wg.Add(1)
		content := strings.Repeat("x", 1024) + string(rune('A'+i))
		go func(body string) {
			defer wg.Done()
			_, writeErr := service.WriteFileIfRevision("chapters/ch01.md", body, baseRev)
			mu.Lock()
			defer mu.Unlock()
			if writeErr == nil {
				successes++
			} else if errors.Is(writeErr, ErrFileRevisionConflict) {
				conflicts++
			} else {
				t.Errorf("unexpected concurrent write error: %v", writeErr)
			}
		}(content)
	}
	wg.Wait()
	if successes != 1 || conflicts != writers-1 {
		t.Fatalf("expected exactly 1 CAS winner and %d conflicts, got successes=%d conflicts=%d", writers-1, successes, conflicts)
	}
}
