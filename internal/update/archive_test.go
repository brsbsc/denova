package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUploadedArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode os.FileMode
		size uint64
	}{
		{"../outside", 0o644, 0},
		{"/absolute", 0o644, 0},
		{`denova\..\outside`, 0o644, 0},
		{"denova/file:stream", 0o644, 0},
		{"denova/link", os.ModeSymlink | 0o777, 0},
		{"denova/huge", 0o644, 2<<30 + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := zip.NewWriter(&archive)
			header := &zip.FileHeader{Name: tc.name, UncompressedSize64: tc.size}
			header.SetMode(tc.mode)
			if _, err := writer.CreateRaw(header); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "upload.zip")
			if err := os.WriteFile(path, archive.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := extractArchive(path, filepath.Join(root, "extract")); err == nil {
				t.Fatal("unsafe entry was accepted")
			}
		})
	}
}

func TestUploadedTarRejectsSymlinksAndExpandedSize(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "denova/link", Typeflag: tar.TypeSymlink, Linkname: "../outside"},
		{Name: "denova/huge", Typeflag: tar.TypeReg, Size: 2<<30 + 1},
	} {
		var archive bytes.Buffer
		gz := gzip.NewWriter(&archive)
		writer := tar.NewWriter(gz)
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		// No expanded payload is needed: the header must be rejected first.
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		path := filepath.Join(root, "upload.tar.gz")
		if err := os.WriteFile(path, archive.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(path, filepath.Join(root, "extract")); err == nil {
			t.Fatal("unsafe tar entry was accepted")
		}
	}
}

func TestUploadedZipRejectsDeflateBombExceedingDeclaredSize(t *testing.T) {
	const actualExpanded = 2 << 20
	var bombArchive bytes.Buffer
	bombWriter := zip.NewWriter(&bombArchive)
	bombEntry, err := bombWriter.Create("denova/bomb.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bombEntry.Write(make([]byte, actualExpanded)); err != nil {
		t.Fatal(err)
	}
	if err := bombWriter.Close(); err != nil {
		t.Fatal(err)
	}
	rawBomb := bytes.Clone(bombArchive.Bytes())
	cdIndex := bytes.LastIndex(rawBomb, []byte{0x50, 0x4b, 0x01, 0x02})
	if cdIndex < 0 || binary.LittleEndian.Uint32(rawBomb[cdIndex+24:cdIndex+28]) != actualExpanded {
		t.Fatalf("unexpected central directory layout (cdIndex=%d)", cdIndex)
	}
	binary.LittleEndian.PutUint32(rawBomb[cdIndex+24:cdIndex+28], 1024)

	root := t.TempDir()
	bombPath := filepath.Join(root, "bomb.zip")
	if err := os.WriteFile(bombPath, rawBomb, 0o644); err != nil {
		t.Fatal(err)
	}
	extractDir := filepath.Join(root, "extract-bomb")
	err = extractArchive(bombPath, extractDir)
	if err == nil || (!errors.Is(err, zip.ErrFormat) && !strings.Contains(err.Error(), "exceeds extraction limits")) {
		t.Fatalf("extractArchive(bomb.zip) err = %v, want zip format or extraction limit error", err)
	}
	if _, statErr := os.Stat(filepath.Join(extractDir, "denova", "bomb.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("partial bomb output remained on disk: err=%v", statErr)
	}

	boundedTarget := filepath.Join(root, "extract-bounded", "oversize.bin")
	err = writeExtractedFile(boundedTarget, bytes.NewReader(make([]byte, 2048)), 0o644, 1024)
	if err == nil || !strings.Contains(err.Error(), "exceeds extraction limits") {
		t.Fatalf("writeExtractedFile oversize err = %v, want extraction limit error", err)
	}
	if _, statErr := os.Stat(boundedTarget); !os.IsNotExist(statErr) {
		t.Fatalf("partial bounded output remained on disk: err=%v", statErr)
	}

	var validArchive bytes.Buffer
	validWriter := zip.NewWriter(&validArchive)
	entry, err := validWriter.Create("denova/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("verified update payload")); err != nil {
		t.Fatal(err)
	}
	if err := validWriter.Close(); err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(root, "valid.zip")
	if err := os.WriteFile(validPath, validArchive.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	validExtract := filepath.Join(root, "extract-valid")
	if err := extractArchive(validPath, validExtract); err != nil {
		t.Fatalf("extractArchive(valid.zip) unexpected error: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(validExtract, "denova", "hello.txt"))
	if err != nil || string(got) != "verified update payload" {
		t.Fatalf("extracted valid payload = %q, err = %v", string(got), err)
	}
}
