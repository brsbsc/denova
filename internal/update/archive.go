package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func extractArchive(archivePath, targetDir string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractZip(archivePath, targetDir)
	}
	if strings.HasSuffix(archivePath, ".tar.gz") {
		return extractTarGz(archivePath, targetDir)
	}
	return fmt.Errorf("unsupported update archive format: %s", filepath.Base(archivePath))
}

const (
	maxArchiveExpandedBytes uint64 = 2 << 30
	maxArchiveEntries              = 30000
)

func extractZip(archivePath, targetDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open zip update archive: %w", err)
	}
	defer reader.Close()
	if len(reader.File) > maxArchiveEntries {
		return errors.New("update archive exceeds extraction limits or contains special files")
	}
	var expandedBytes uint64
	for _, f := range reader.File {
		remaining := maxArchiveExpandedBytes - expandedBytes
		if f.UncompressedSize64 > remaining || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return errors.New("update archive exceeds extraction limits or contains special files")
		}
		expandedBytes += f.UncompressedSize64
		target, err := safeJoin(targetDir, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		entryLimit := int64(min(remaining, f.UncompressedSize64))
		if err := writeExtractedFile(target, rc, f.FileInfo().Mode(), entryLimit); err != nil {
			rc.Close()
			return err
		}
		if err := rc.Close(); err != nil {
			_ = os.Remove(target)
			return err
		}
	}
	return nil
}

func extractTarGz(archivePath, targetDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open tar.gz update archive: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read gzip update archive: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	var expandedBytes int64
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if header.Size < 0 || header.Size > int64(maxArchiveExpandedBytes)-expandedBytes || entries > maxArchiveEntries {
			return errors.New("update archive exceeds extraction limits")
		}
		expandedBytes += header.Size
		target, err := safeJoin(targetDir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeExtractedFile(target, reader, header.FileInfo().Mode(), header.Size); err != nil {
				return err
			}
		default:
			return errors.New("update archive contains special files")
		}
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	if strings.Contains(name, `\`) || strings.Contains(name, ":") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("update archive contains an invalid path: %s", name)
	}
	target := filepath.Join(root, filepath.Clean(name))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("update archive contains an invalid path: %s", name)
	}
	return target, nil
}

func writeExtractedFile(target string, reader io.Reader, mode os.FileMode, maxBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(out, io.LimitReader(reader, maxBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if written > maxBytes {
		_ = os.Remove(target)
		return errors.New("update archive exceeds extraction limits")
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}
