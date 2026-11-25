package style

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"denova/internal/portablepath"
	"golang.org/x/text/unicode/norm"
)

func NormalizeStoragePath(path string) string {
	path = strings.TrimSpace(filepath.ToSlash(path))
	if path == "" {
		return ""
	}
	base := strings.TrimSpace(filepath.Base(path))
	if base == "" || strings.HasPrefix(base, ".") {
		return ""
	}
	if !isStyleFile(base) {
		base += ".md"
	}
	base = sanitizeFilename(base)
	if base == "" {
		return ""
	}
	return StoragePath(base)
}

func StoragePath(filename string) string {
	raw := strings.TrimSpace(filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")))
	if raw == "" || strings.HasPrefix(raw, ".") {
		return ""
	}
	filename = sanitizeFilename(raw)
	if filename == "" {
		return ""
	}
	if !isStyleFile(filename) {
		filename += ".md"
	}
	normalized, err := portablepath.NormalizeComponent(filename)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(filepath.Join(DisplayDir, normalized))
}

func isStyleFile(name string) bool {
	trimmed := strings.TrimSpace(name)
	ext := strings.ToLower(filepath.Ext(trimmed))
	if ext != ".md" && ext != ".markdown" && ext != ".txt" {
		return false
	}
	return strings.TrimSuffix(trimmed, filepath.Ext(trimmed)) != ""
}

func filenameForWrite(filename, name string) string {
	raw := strings.TrimSpace(filepath.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/")))
	if strings.HasPrefix(raw, ".") {
		filename = ""
	} else {
		filename = sanitizeFilename(raw)
	}
	if filename != "" {
		if !isStyleFile(filename) {
			filename += ".md"
		}
		if strings.ToLower(filepath.Ext(filename)) != ".md" {
			filename = strings.TrimSuffix(filename, filepath.Ext(filename)) + ".md"
		}
		if normalized, err := portablepath.NormalizeComponent(filename); err == nil {
			filename = normalized
		} else {
			filename = ""
		}
	}
	if filename == "" {
		filename = slugFilename(name)
	}
	if filename == "" {
		filename = fmt.Sprintf("style-%d.md", time.Now().UnixNano())
	}
	return filename
}

func sanitizeFilename(filename string) string {
	filename = norm.NFC.String(strings.TrimSpace(filename))
	filename = strings.ReplaceAll(filename, "\\", "/")
	filename = filepath.Base(filename)
	if strings.HasPrefix(filename, ".") {
		return ""
	}
	filename = strings.Trim(filename, ". ")
	if filename == "" || filename == "." || filename == ".." {
		return ""
	}
	var out strings.Builder
	for _, r := range filename {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			out.WriteRune(r)
		case r == '.', r == '-', r == '_':
			out.WriteRune(r)
		case unicode.IsSpace(r):
			out.WriteByte('-')
		}
	}
	cleaned := norm.NFC.String(strings.Trim(out.String(), ".- _"))
	if cleaned == "" {
		return ""
	}
	ext := ""
	stem := cleaned
	if isStyleFile(cleaned) {
		ext = filepath.Ext(cleaned)
		stem = strings.Trim(strings.TrimSuffix(cleaned, ext), ".- _")
	}
	if stem == "" {
		return ""
	}
	checkExt := ext
	if checkExt == "" {
		checkExt = ".md"
	}
	stem = truncateStyleStemBytes(stem, portablepath.MaxComponentBytes-len(checkExt))
	if stem == "" {
		return ""
	}
	if err := portablepath.ValidateComponent(stem + checkExt); err != nil {
		return ""
	}
	return stem + ext
}

func slugFilename(name string) string {
	name = norm.NFC.String(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	var out strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsNumber(r):
			out.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r), r == '-', r == '_':
			if !lastDash && out.Len() > 0 {
				out.WriteByte('-')
				lastDash = true
			}
		}
	}
	stem := truncateStyleStemBytes(norm.NFC.String(strings.Trim(out.String(), "-")), portablepath.MaxComponentBytes-len(".md"))
	if stem == "" {
		return ""
	}
	candidate := stem + ".md"
	if normalized, err := portablepath.NormalizeComponent(candidate); err == nil {
		return normalized
	}
	return ""
}

func truncateStyleStemBytes(stem string, maxBytes int) string {
	if maxBytes <= 0 || stem == "" {
		return ""
	}
	if len(stem) <= maxBytes {
		return stem
	}
	end := 0
	for index, r := range stem {
		next := index + utf8.RuneLen(r)
		if next > maxBytes {
			break
		}
		end = next
	}
	return norm.NFC.String(strings.Trim(stem[:end], ".- _"))
}

func summarizeMarkdown(filename, content string) (string, string) {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	desc := ""
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" || trimmed == "---" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && name == strings.TrimSuffix(filename, filepath.Ext(filename)) {
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		if trimmed != "" {
			desc = truncateRunes(trimmed, MaxDescriptionSize)
			break
		}
	}
	if strings.TrimSpace(name) == "" {
		name = strings.TrimSuffix(filename, filepath.Ext(filename))
	}
	return strings.TrimSpace(name), desc
}

func ensureTrailingNewline(value string) string {
	if strings.HasSuffix(value, "\n") {
		return value
	}
	return value + "\n"
}

func ensureReferenceHeader(content, name, description string) string {
	content = strings.TrimSpace(content)
	if hasMarkdownH1(content) {
		return content
	}
	name = oneLine(name)
	description = truncateRunes(oneLine(description), MaxDescriptionSize)
	if name == "" && description == "" {
		return content
	}
	var sb strings.Builder
	if name != "" {
		fmt.Fprintf(&sb, "# %s\n\n", name)
	}
	if description != "" {
		fmt.Fprintf(&sb, "> %s\n\n", description)
	}
	sb.WriteString(content)
	return sb.String()
}

func hasMarkdownH1(content string) bool {
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			return true
		}
	}
	return false
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
