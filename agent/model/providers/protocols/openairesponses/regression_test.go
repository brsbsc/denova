package openairesponses

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

func TestTruncatedAndFailedResponsesStreamsSetInStreamRetryability(t *testing.T) {
	t.Run("truncated_eof", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.Header().Set("X-Request-ID", "req-trunc-resp")
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"m","output":[]}}`)
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", `{"type":"response.output_text.delta","sequence_number":1,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial","logprobs":[]}`)
		}))
		defer server.Close()

		model, err := NewAdapter().New(context.Background(), providers.ModelConfig{
			Provider: providers.ProviderOpenAI, Protocol: providers.ProtocolOpenAIResponses,
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
		if !errors.As(streamErr, &apiErr) || !apiErr.InStream || !apiErr.Retryable() || apiErr.RequestID != "req-trunc-resp" {
			t.Fatalf("truncated stream error = %#v (raw=%v)", apiErr, streamErr)
		}
	})

	t.Run("response_failed_event", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", `{"type":"response.failed","sequence_number":1,"response":{"id":"resp_fail","object":"response","created_at":1,"status":"failed","model":"m","error":{"code":"upstream_overloaded","message":"overloaded"},"output":[]}}`)
		}))
		defer server.Close()

		model, err := NewAdapter().New(context.Background(), providers.ModelConfig{
			Provider: providers.ProviderOpenAI, Protocol: providers.ProtocolOpenAIResponses,
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
		_, recvErr := stream.Recv()
		var apiErr *providers.APIError
		if !errors.As(recvErr, &apiErr) || !apiErr.InStream || !apiErr.Retryable() {
			t.Fatalf("response.failed error = %#v (raw=%v)", apiErr, recvErr)
		}
	})
}
