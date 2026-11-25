package schema

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MaxAttachmentImageBytes bounds native image reads before SHA-256 verification.
const MaxAttachmentImageBytes = 20 << 20

// Attachment is a durable, provider-neutral copy supplied by a user or a tool.
// User paths are relative to Definition.AttachmentRoot; tool image paths are
// relative to Definition.Artifacts' boundary. RuntimePath is reconstructed for
// the current host and is deliberately never persisted.
type Attachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MediaType   string `json:"media_type,omitempty"`
	Size        int64  `json:"size"`
	Path        string `json:"path,omitempty"`
	RuntimePath string `json:"-"`
	// SHA256 binds provider input to the immutable bytes originally observed.
	SHA256 string `json:"sha256,omitempty"`
}

// IsNativeImageMediaType reports whether every built-in multimodal protocol
// can safely represent the image as a native input part.
func IsNativeImageMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// UserMessageWithAttachments constructs one canonical user input without
// changing the user-authored text.
func UserMessageWithAttachments(content string, attachments []Attachment) *Message {
	return &Message{Role: User, Content: content, Attachments: CloneAttachments(attachments)}
}

// ModelUserContent appends the stable filesystem contract for attached copies.
// Native image adapters also send supported images as image content parts.
func ModelUserContent(message *Message) string {
	if message == nil || len(message.Attachments) == 0 {
		if message == nil {
			return ""
		}
		return message.Content
	}
	var builder strings.Builder
	if content := strings.TrimSpace(message.Content); content != "" {
		builder.WriteString(content)
		builder.WriteString("\n\n")
	}
	builder.WriteString("# Attached files\n\n")
	builder.WriteString("These are immutable input copies owned by the application. Read them with available filesystem or shell tools when useful. Never modify or delete an attached input. To edit its content, copy it into the workspace or create a new output artifact.\n")
	for _, attachment := range message.Attachments {
		builder.WriteString("\n- name: ")
		builder.WriteString(strconv.Quote(attachment.Name))
		builder.WriteString("\n  path: ")
		builder.WriteString(strconv.Quote(AttachmentFilePath(attachment)))
		if attachment.MediaType != "" {
			builder.WriteString("\n  media_type: ")
			builder.WriteString(strconv.Quote(attachment.MediaType))
		}
		builder.WriteString("\n  size_bytes: ")
		builder.WriteString(strconv.FormatInt(attachment.Size, 10))
	}
	return builder.String()
}

// AttachmentDataURL loads an application-owned image copy at provider request
// time, keeping binary payloads out of the durable transcript.
func AttachmentDataURL(attachment Attachment) (string, error) {
	if !IsNativeImageMediaType(attachment.MediaType) {
		return "", fmt.Errorf("attachment %q is not a supported native image", attachment.Name)
	}
	encoded, err := AttachmentBase64(attachment)
	if err != nil {
		return "", err
	}
	return "data:" + attachment.MediaType + ";base64," + encoded, nil
}

// AttachmentBase64 loads one native image for protocols whose source schema
// carries media type and base64 data separately.
func AttachmentBase64(attachment Attachment) (string, error) {
	data, err := ReadAttachmentImage(attachment)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// ReadAttachmentImage reads and verifies the immutable native image. Providers
// may derive an in-memory sending copy, but must never overwrite this input.
func ReadAttachmentImage(attachment Attachment) ([]byte, error) {
	if !IsNativeImageMediaType(attachment.MediaType) {
		return nil, fmt.Errorf("attachment %q is not a supported native image", attachment.Name)
	}
	file, err := os.Open(AttachmentFilePath(attachment))
	if err != nil {
		return nil, fmt.Errorf("read attached image %q: %w", attachment.Name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat attached image %q: %w", attachment.Name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attached image %q must be a regular file", attachment.Name)
	}
	if info.Size() > MaxAttachmentImageBytes {
		return nil, fmt.Errorf("attached image %q exceeds the %d-byte limit", attachment.Name, MaxAttachmentImageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxAttachmentImageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read attached image %q: %w", attachment.Name, err)
	}
	if int64(len(data)) > MaxAttachmentImageBytes {
		return nil, fmt.Errorf("attached image %q exceeds the %d-byte limit", attachment.Name, MaxAttachmentImageBytes)
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), strings.TrimSpace(attachment.SHA256)) {
		return nil, fmt.Errorf("attached image %q immutable copy changed", attachment.Name)
	}
	return data, nil
}

func CloneAttachments(values []Attachment) []Attachment {
	return append([]Attachment(nil), values...)
}

func ValidateToolAttachments(attachments []Attachment) error {
	if len(attachments) > MaxToolResultArtifacts {
		return fmt.Errorf("tool result has %d images; maximum is %d", len(attachments), MaxToolResultArtifacts)
	}
	for index, attachment := range attachments {
		digest, err := hex.DecodeString(attachment.SHA256)
		if attachment.ID == "" || attachment.Name == "" || attachment.Size <= 0 ||
			!IsNativeImageMediaType(attachment.MediaType) || err != nil || len(digest) != sha256.Size ||
			!fs.ValidPath(attachment.Path) || attachment.Path == "." || strings.ContainsAny(attachment.Path, "\\:\x00") {
			return fmt.Errorf("tool image %d requires a native image type, SHA-256, and an owner-relative immutable copy", index)
		}
	}
	encoded, err := json.Marshal(attachments)
	if err != nil || len(encoded) > MaxToolResultArtifactMetadataBytes {
		return fmt.Errorf("tool image metadata exceeds %d bytes", MaxToolResultArtifactMetadataBytes)
	}
	return nil
}

func AttachmentsFromMessages(messages []*Message) []Attachment {
	var attachments []Attachment
	seen := make(map[string]struct{})
	for _, message := range messages {
		if message == nil {
			continue
		}
		for _, attachment := range message.Attachments {
			path := AttachmentFilePath(attachment)
			if path == "" {
				continue
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			attachments = append(attachments, attachment)
		}
	}
	return attachments
}

func AttachmentFilePath(attachment Attachment) string {
	if path := strings.TrimSpace(attachment.RuntimePath); path != "" {
		return path
	}
	return strings.TrimSpace(attachment.Path)
}

// resolveMessageAttachmentPaths projects durable slash-relative paths onto
// the current host without changing the persisted Attachment.Path value.
func ResolveMessageAttachmentPaths(root string, messages []*Message) ([]*Message, error) {
	root = strings.TrimSpace(root)
	var absoluteRoot string
	if root != "" {
		var err error
		absoluteRoot, err = filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve attachment root: %w", err)
		}
	}
	result := CloneMessages(messages)
	for _, message := range result {
		if message == nil || message.Role == ToolRole {
			continue
		}
		for index := range message.Attachments {
			attachment := &message.Attachments[index]
			stored := strings.TrimSpace(attachment.Path)
			if stored == "" {
				continue
			}
			// Absolute paths without an AttachmentRoot are accepted only for
			// in-memory SDK callers. Once AttachmentRoot is configured, every
			// resolved path must stay strictly inside AttachmentRoot.
			if root == "" {
				if filepath.IsAbs(stored) || strings.HasPrefix(stored, "/") {
					attachment.RuntimePath = filepath.Clean(stored)
					continue
				}
				return nil, fmt.Errorf("resolve attachment %q: AttachmentRoot is required", attachment.Name)
			}
			var candidate string
			if filepath.IsAbs(stored) || strings.HasPrefix(stored, "/") || filepath.VolumeName(stored) != "" {
				if !filepath.IsAbs(stored) {
					return nil, fmt.Errorf("resolve attachment %q: durable path escapes AttachmentRoot", attachment.Name)
				}
				candidate = filepath.Clean(stored)
			} else {
				if strings.Contains(stored, "\\") || !fs.ValidPath(stored) || stored == "." {
					return nil, fmt.Errorf("resolve attachment %q: invalid durable path %q", attachment.Name, stored)
				}
				candidate = filepath.Join(absoluteRoot, filepath.FromSlash(stored))
			}
			if err := verifyAttachmentInsideRoot(absoluteRoot, candidate, attachment.Name); err != nil {
				return nil, err
			}
			attachment.RuntimePath = candidate
		}
	}
	return result, nil
}

func verifyAttachmentInsideRoot(absoluteRoot, candidate, name string) error {
	relative, err := filepath.Rel(absoluteRoot, candidate)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("resolve attachment %q: durable path escapes AttachmentRoot", name)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect attachment %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("resolve attachment %q: symbolic links are not allowed", name)
	}
	canonicalRoot, rootErr := filepath.EvalSymlinks(absoluteRoot)
	canonicalCandidate, candidateErr := filepath.EvalSymlinks(candidate)
	if rootErr == nil && candidateErr == nil {
		canonicalRel, relErr := filepath.Rel(canonicalRoot, canonicalCandidate)
		if relErr != nil || canonicalRel == "." || canonicalRel == ".." || strings.HasPrefix(canonicalRel, ".."+string(filepath.Separator)) || filepath.IsAbs(canonicalRel) {
			return fmt.Errorf("resolve attachment %q: durable path escapes AttachmentRoot", name)
		}
	}
	return nil
}
