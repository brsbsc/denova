package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMessageAssemblerIncrementalMatchesConcatAndPreservesPrefixOnError(t *testing.T) {
	assembler := NewMessageAssembler()
	idx0, idx1 := 0, 1
	chunks := []*Message{
		{Role: Assistant, Extra: map[string]any{"provider": "openai", "stream": "part1-"}},
		{Role: Assistant, ReasoningContent: "think-1 "},
		{Role: Assistant, Content: "hello ", ReasoningContent: "think-2"},
		{Role: Assistant, ToolCalls: []ToolCall{{Index: &idx0, ID: "call_0", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{"path":`}}}},
		{Role: Assistant, ToolCalls: []ToolCall{{Index: &idx1, ID: "call_1", Type: "function", Function: FunctionCall{Name: "glob", Arguments: `{"paths":["."]}`}}}},
		{Role: Assistant, Content: "world", ToolCalls: []ToolCall{{Index: &idx0, Function: FunctionCall{Arguments: `"a.md"}`}}}},
		{Role: Assistant, Extra: map[string]any{"stream": "part2"}, ResponseMeta: &ResponseMeta{FinishReason: "tool_calls", Usage: &TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}},
	}
	for i, chunk := range chunks {
		if err := assembler.Append(chunk); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	// Conflicting chunks must not corrupt the already accepted prefix.
	if err := assembler.Append(&Message{Role: User, Content: "bad"}); err == nil {
		t.Fatal("expected conflicting role error")
	}
	if err := assembler.Append(&Message{Role: Assistant, ToolCalls: []ToolCall{{Index: &idx0, ID: "other_id"}}}); err == nil {
		t.Fatal("expected conflicting tool call ID error")
	}
	if err := assembler.Append(&Message{Role: Assistant, Extra: map[string]any{"provider": 123}}); err == nil {
		t.Fatal("expected conflicting extra type error")
	}

	got, err := assembler.Message()
	if err != nil {
		t.Fatal(err)
	}
	want, err := ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incremental message mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestResolveMessageAttachmentPathsRejectsRootEscapeAndBoundsRead(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	inside := filepath.Join(root, "attachments", "ok.png")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("png-bytes")
	if err := os.WriteFile(inside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	// Legacy absolute path outside AttachmentRoot must be rejected when root is configured.
	_, err := ResolveMessageAttachmentPaths(root, []*Message{
		UserMessageWithAttachments("inspect", []Attachment{{Name: "secret.png", Path: outside}}),
	})
	if err == nil || !strings.Contains(err.Error(), "escapes AttachmentRoot") {
		t.Fatalf("expected outside absolute path rejection, got %v", err)
	}

	// Relative path and legacy absolute path inside AttachmentRoot both succeed.
	resolved, err := ResolveMessageAttachmentPaths(root, []*Message{
		UserMessageWithAttachments("inspect", []Attachment{
			{Name: "rel.png", Path: "attachments/ok.png"},
			{Name: "abs-in-root.png", Path: inside},
		}),
	})
	if err != nil {
		t.Fatalf("resolve valid attachments: %v", err)
	}
	if resolved[0].Attachments[0].RuntimePath != inside || resolved[0].Attachments[1].RuntimePath != inside {
		t.Fatalf("unexpected runtime paths: %#v", resolved[0].Attachments)
	}

	// Oversized attachment file is rejected before reading unbounded bytes.
	oversized := filepath.Join(t.TempDir(), "oversized.png")
	f, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxAttachmentImageBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	sum := sha256.Sum256(nil)
	_, err = ReadAttachmentImage(Attachment{
		Name: "oversized.png", MediaType: "image/png", Path: oversized, SHA256: hex.EncodeToString(sum[:]),
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size limit error, got %v", err)
	}
}
