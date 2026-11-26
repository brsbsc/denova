package revisionfile

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"denova/internal/localfs"
)

// Keep the temporary sibling filename well within Linux/POSIX 255-byte NAME_MAX
// even when the target filename reaches portablepath.MaxComponentBytes (240 bytes).
const maxTempBaseBytes = 200

func tempPatternFor(base string) string {
	if len(base) > maxTempBaseBytes {
		end := 0
		for i, r := range base {
			next := i + utf8.RuneLen(r)
			if next > maxTempBaseBytes {
				break
			}
			end = next
		}
		base = base[:end]
	}
	return "." + base + ".denova-*"
}

func atomicReplace(path string, content []byte, fileMode, directoryMode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, directoryMode); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, tempPatternFor(filepath.Base(path)))
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err = temp.Chmod(fileMode); err != nil {
		return err
	}
	if _, err = temp.Write(content); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	if err := localfs.SyncDirectory(dir); err != nil {
		return fmt.Errorf("sync revision file directory: %w", err)
	}
	return nil
}
