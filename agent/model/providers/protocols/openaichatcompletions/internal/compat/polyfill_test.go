package compat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	agentmodel "github.com/alfredxw/denova/agent/model"
	agentstream "github.com/alfredxw/denova/agent/model/stream"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

type stubCompatModel struct {
	frames []*agentschema.Message
}

func (m *stubCompatModel) Generate(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentschema.Message, error) {
	return agentschema.ConcatMessages(m.frames)
}

func (m *stubCompatModel) Stream(context.Context, []*agentschema.Message, ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	return agentstream.StreamReaderFromArray(m.frames), nil
}

func (m *stubCompatModel) WithTools([]*agentschema.ToolInfo) (agentmodel.ToolCallingChatModel, error) {
	return m, nil
}

func TestStreamPolyfillAssignsSequentialToolIndicesAndDedupsMetadata(t *testing.T) {
	wrapped := Wrap(&stubCompatModel{
		frames: []*agentschema.Message{
			{
				Role:             agentschema.Assistant,
				ReasoningContent: "pre-think ",
				Content:          "<think>inline-think",
				Extra:            map[string]any{"provider": "compat-provider", "response_id": "resp-1"},
			},
			{
				Role:    agentschema.Assistant,
				Content: "</think>lead <tool_call><invoke name=\"first\"><parameter name=\"a\">1</parameter></invoke></tool_call> mid <tool_call><invoke name=\"second\"><parameter name=\"b\">2</parameter></invoke></tool_call>",
				ResponseMeta: &agentschema.ResponseMeta{
					FinishReason: "stop",
					Usage:        &agentschema.TokenUsage{PromptTokens: 4, CompletionTokens: 8, TotalTokens: 12},
				},
			},
		},
	}, Config{
		Model:                "test-compat",
		RepairTextToolCalls:  true,
		RepairInlineThinking: true,
	})

	reader, err := wrapped.Stream(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var out []*agentschema.Message
	for {
		msg, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("recv: %v", recvErr)
		}
		out = append(out, msg)
	}

	merged, err := agentschema.ConcatMessages(out)
	if err != nil {
		t.Fatalf("ConcatMessages failed on polyfilled stream: %v", err)
	}
	if len(merged.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2: %#v", len(merged.ToolCalls), merged.ToolCalls)
	}
	if *merged.ToolCalls[0].Index != 0 || merged.ToolCalls[0].Function.Name != "first" ||
		*merged.ToolCalls[1].Index != 1 || merged.ToolCalls[1].Function.Name != "second" {
		t.Fatalf("unexpected tool calls: %#v", merged.ToolCalls)
	}
	if merged.Extra["provider"] != "compat-provider" || merged.Extra["response_id"] != "resp-1" {
		t.Fatalf("metadata duplicated or dropped: %#v", merged.Extra)
	}
	if merged.ReasoningContent != "pre-think inline-think" {
		t.Fatalf("reasoning = %q", merged.ReasoningContent)
	}
	if merged.ResponseMeta == nil || merged.ResponseMeta.FinishReason != "stop" || merged.ResponseMeta.Usage.TotalTokens != 12 {
		t.Fatalf("response meta = %#v", merged.ResponseMeta)
	}
}

func TestShouldFilterSSEResponseRespectsExplicitNonSSEContentType(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")

	jsonResp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}}
	if shouldFilterSSEResponse(req, jsonResp) {
		t.Fatal("explicit application/json response must not be filtered as SSE")
	}

	sseResp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream; charset=utf-8"}}}
	if !shouldFilterSSEResponse(req, sseResp) {
		t.Fatal("text/event-stream response should be filtered")
	}
}
