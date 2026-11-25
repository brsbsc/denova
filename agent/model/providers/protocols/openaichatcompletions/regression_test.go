package openaichatcompletions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alfredxw/denova/agent/model/providers"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

func TestTruncatedChatCompletionsStreamWithoutFinishReasonFailsRetryably(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Request-ID", "req-trunc-chat")
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", `{"id":"stream-id","object":"chat.completion.chunk","created":2,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"partial"},"finish_reason":""}]}`)
	}))
	defer server.Close()

	model, err := newTestModel(context.Background(), &providers.ModelConfig{
		Model: "m", BaseURL: server.URL, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), []*agentschema.Message{agentschema.UserMessage("hi")})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var streamErr error
	for {
		if _, recvErr := stream.Recv(); recvErr != nil {
			streamErr = recvErr
			break
		}
	}
	var apiErr *providers.APIError
	if !errors.As(streamErr, &apiErr) || !apiErr.InStream || !apiErr.Retryable() || apiErr.RequestID != "req-trunc-chat" {
		t.Fatalf("truncated chat stream error = %#v (raw=%v)", apiErr, streamErr)
	}
}
