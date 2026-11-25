package anthropicmessages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alfredxw/denova/agent/model/providers"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

func TestRequestMessagesCoalescesConsecutiveToolRoleTurns(t *testing.T) {
	config := providers.ModelConfig{
		Provider: "anthropic",
		Protocol: providers.ProtocolAnthropicMessages,
		Model:    "claude-sonnet-4-5",
	}
	input := []*agentschema.Message{
		agentschema.UserMessage("run two tools"),
		agentschema.AssistantMessage("", []agentschema.ToolCall{
			{ID: "toolu_1", Type: "function", Function: agentschema.FunctionCall{Name: "read", Arguments: `{"path":"a.md"}`}},
			{ID: "toolu_2", Type: "function", Function: agentschema.FunctionCall{Name: "read", Arguments: `{"path":"b.md"}`}},
		}),
		{
			Role:        agentschema.ToolRole,
			ToolCallID:  "toolu_1",
			Content:     "content-a",
			Attachments: []agentschema.Attachment{{Name: "notes.txt", MediaType: "text/plain", Path: "notes.txt"}},
		},
		{
			Role:       agentschema.ToolRole,
			ToolCallID: "toolu_2",
			Content:    "content-b",
			ToolResult: &agentschema.ToolResultSummary{Status: agentschema.ToolResultError},
		},
	}
	_, messages, err := requestMessages(input, config)
	if err != nil {
		t.Fatalf("requestMessages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("len(messages) = %d, want 3 (user, assistant, coalesced tool-result user)", len(messages))
	}
	encoded, err := json.Marshal(messages[2])
	if err != nil {
		t.Fatal(err)
	}
	var turn struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(encoded, &turn); err != nil {
		t.Fatal(err)
	}
	if turn.Role != "user" || len(turn.Content) != 2 {
		t.Fatalf("coalesced turn = %#v", turn)
	}
	if turn.Content[0]["tool_use_id"] != "toolu_1" || turn.Content[1]["tool_use_id"] != "toolu_2" || turn.Content[1]["is_error"] != true {
		t.Fatalf("unexpected tool_result blocks: %#v", turn.Content)
	}
}

func TestTruncatedAnthropicSSEWithoutMessageStopFailsRetryably(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Request-Id", "req-trunc")
		_, _ = fmt.Fprintf(writer, "event: message_start\ndata: %s\n\n", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"test-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`)
		_, _ = fmt.Fprintf(writer, "event: content_block_start\ndata: %s\n\n", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		_, _ = fmt.Fprintf(writer, "event: content_block_delta\ndata: %s\n\n", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`)
	}))
	defer server.Close()

	model, err := NewAdapter().New(context.Background(), providers.ModelConfig{
		Provider: "anthropic", Protocol: providers.ProtocolAnthropicMessages,
		BaseURL: server.URL, Model: "test-model", APIKey: "secret", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, genErr := model.Generate(context.Background(), []*agentschema.Message{agentschema.UserMessage("hi")})
	var apiErr *providers.APIError
	if !errors.As(genErr, &apiErr) || !apiErr.InStream || !apiErr.Retryable() || apiErr.RequestID != "req-trunc" {
		t.Fatalf("Generate truncated error = %#v (raw=%v)", apiErr, genErr)
	}

	stream, err := model.Stream(context.Background(), []*agentschema.Message{agentschema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var streamErr error
	for {
		_, recvErr := stream.Recv()
		if recvErr != nil {
			if !errors.Is(recvErr, context.Canceled) {
				streamErr = recvErr
			}
			break
		}
	}
	apiErr = nil
	if !errors.As(streamErr, &apiErr) || !apiErr.InStream || !apiErr.Retryable() {
		t.Fatalf("Stream truncated error = %#v (raw=%v)", apiErr, streamErr)
	}
}
