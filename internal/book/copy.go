package book

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"denova/internal/portablepath"
)

// CopyPath 复制文件或目录，拒绝符号链接及自包含目标路径。
func CopyPath(from, to string) error {
	if sameOrDescendantHostPath(from, to) {
		return errors.New("不能将目录或文件复制到自身或其子目录中")
	}
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("不支持复制符号链接: %s", from)
	}
	if info.IsDir() {
		return copyDir(from, to, info.Mode())
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("不支持复制非常规文件: %s", from)
	}
	return copyFile(from, to, info.Mode())
}

// copyDir 递归复制目录，跳过隐藏文件和隐藏目录，拒绝符号链接。
func copyDir(from, to string, mode os.FileMode) (retErr error) {
	_, existedErr := os.Lstat(to)
	createdDir := errors.Is(existedErr, os.ErrNotExist)
	if err := os.MkdirAll(to, mode.Perm()); err != nil {
		return err
	}
	defer func() {
		if retErr != nil && createdDir {
			_ = os.RemoveAll(to)
		}
	}()

	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: 不支持复制符号链接", entry.Name())
		}
		src := filepath.Join(from, entry.Name())
		dst := filepath.Join(to, entry.Name())
		if err := CopyPath(src, dst); err != nil {
			return fmt.Errorf("%s: %w", entry.Name(), err)
		}
	}
	return nil
}

// copyFile 复制单个文件并保留权限；写入或关闭失败时清理半成品目标文件。
func copyFile(from, to string, mode os.FileMode) (retErr error) {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}

	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = dst.Close()
		}
		if retErr != nil {
			_ = os.Remove(to)
		}
	}()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	closed = true
	if err := dst.Close(); err != nil {
		return err
	}
	return nil
}

func sameOrDescendantHostPath(from, to string) bool {
	absFrom, errFrom := filepath.Abs(from)
	absTo, errTo := filepath.Abs(to)
	if errFrom != nil || errTo != nil {
		return false
	}
	foldHost := func(p string) string {
		clean := filepath.ToSlash(filepath.Clean(p))
		parts := strings.Split(clean, "/")
		for i, part := range parts {
			parts[i] = portablepath.FoldKey(part)
		}
		return strings.Join(parts, "/")
	}
	fromKey := foldHost(absFrom)
	toKey := foldHost(absTo)
	return toKey == fromKey || strings.HasPrefix(toKey, fromKey+"/")
}
