package anthropicmessages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"

	agentmodel "github.com/alfredxw/denova/agent/model"
	"github.com/alfredxw/denova/agent/model/providers"
	"github.com/alfredxw/denova/agent/model/providers/protocols/internal/protocolutil"
	agentstream "github.com/alfredxw/denova/agent/model/stream"
	agentschema "github.com/alfredxw/denova/agent/schema"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Generate deliberately consumes the streaming endpoint. The Anthropic SDK's
// non-streaming helper injects a model-dependent timeout; Denova leaves model
// execution unbounded except for caller context cancellation.
func (model *ChatModel) Generate(ctx context.Context, input []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentschema.Message, error) {
	opts = agentmodel.BindContextSessionKey(ctx, model.options, opts...)
	params, requestOptions, err := model.request(input, opts...)
	if err != nil {
		return nil, err
	}
	callCtx, fallback := withJSONMessageFallback(ctx)
	var rawResponse *http.Response
	requestOptions = append(requestOptions, option.WithResponseInto(&rawResponse))
	stream := model.client.Messages.NewStreaming(callCtx, params, requestOptions...)
	defer stream.Close()
	accumulator := &anthropic.Message{}
	sawStop := false
	for stream.Next() {
		event := stream.Current()
		if event.Type == "message_stop" {
			sawStop = true
		}
		if err := accumulator.Accumulate(event); err != nil {
			return nil, fmt.Errorf("anthropic messages stream accumulation: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, protocolutil.ModelCallError(ctx, err, adaptAPIError)
	}
	if !sawStop {
		if direct, ok, fallbackErr := fallback.decode(ctx, rawResponse, model.config); fallbackErr != nil || ok {
			return direct, fallbackErr
		}
		return nil, truncatedAnthropicStreamError(rawResponse)
	}
	return responseMessage(accumulator, rawResponse, model.config)
}

func (model *ChatModel) Stream(ctx context.Context, input []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	opts = agentmodel.BindContextSessionKey(ctx, model.options, opts...)
	params, requestOptions, err := model.request(input, opts...)
	if err != nil {
		return nil, err
	}
	callCtx, fallback := withJSONMessageFallback(ctx)
	var rawResponse *http.Response
	requestOptions = append(requestOptions, option.WithResponseInto(&rawResponse))
	providerStream := model.client.Messages.NewStreaming(callCtx, params, requestOptions...)
	if err := providerStream.Err(); err != nil {
		_ = providerStream.Close()
		return nil, protocolutil.ModelCallError(ctx, err, adaptAPIError)
	}
	reader, writer := agentstream.Pipe[*agentschema.Message](1)
	go func() {
		defer writer.Close()
		defer providerStream.Close()
		defer func() {
			if value := recover(); value != nil {
				writer.Send(nil, &agentstream.PanicError{Value: value, Stack: append([]byte(nil), debug.Stack()...)})
			}
		}()
		accumulator := &anthropic.Message{}
		sawStop := false
		for providerStream.Next() {
			event := providerStream.Current()
			if event.Type == "message_stop" {
				sawStop = true
			}
			if err := accumulator.Accumulate(event); err != nil {
				writer.Send(nil, fmt.Errorf("anthropic messages stream accumulation: %w", err))
				return
			}
			message, err := streamMessage(event, accumulator, rawResponse, model.config)
			if err != nil {
				writer.Send(nil, err)
				return
			}
			if message != nil && writer.Send(message, nil) {
				return
			}
		}
		if err := providerStream.Err(); err != nil {
			writer.Send(nil, protocolutil.ModelCallError(ctx, err, adaptAPIError))
			return
		}
		if !sawStop {
			if direct, ok, fallbackErr := fallback.decode(ctx, rawResponse, model.config); fallbackErr != nil || ok {
				writer.Send(direct, fallbackErr)
				return
			}
			writer.Send(nil, truncatedAnthropicStreamError(rawResponse))
		}
	}()
	return reader, nil
}

func (f *jsonMessageFallback) decode(ctx context.Context, rawResponse *http.Response, config providers.ModelConfig) (*agentschema.Message, bool, error) {
	if f == nil || !f.captured {
		return nil, false, nil
	}
	if f.readErr != nil {
		return nil, false, protocolutil.ModelCallError(ctx, f.readErr, adaptAPIError)
	}
	var direct anthropic.Message
	if err := json.Unmarshal(f.body, &direct); err != nil || direct.Type != "message" {
		return nil, false, truncatedAnthropicStreamError(rawResponse)
	}
	msg, err := responseMessage(&direct, rawResponse, config)
	if err != nil {
		return nil, false, err
	}
	return msg, true, nil
}

func (model *ChatModel) WithTools(tools []*agentschema.ToolInfo) (agentmodel.ToolCallingChatModel, error) {
	if model == nil {
		return nil, fmt.Errorf("anthropic messages: nil receiver")
	}
	clone := *model
	clone.options = agentmodel.GetCommonOptions(model.options, agentmodel.WithTools(tools))
	return &clone, nil
}

func truncatedAnthropicStreamError(rawResponse *http.Response) error {
	return &providers.APIError{
		RequestID: responseRequestID(rawResponse),
		Kind:      "server_error",
		InStream:  true,
		Message:   "anthropic messages stream ended before message_stop",
	}
}
