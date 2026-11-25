package anthropicmessages

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	agentmodel "github.com/alfredxw/denova/agent/model"
	"github.com/alfredxw/denova/agent/model/providers"
	"github.com/alfredxw/denova/agent/model/providers/protocols/internal/llmhttp"
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

type Adapter struct{}

func NewAdapter() *Adapter { return &Adapter{} }

func (*Adapter) ID() providers.ProtocolID { return providers.ProtocolAnthropicMessages }

type ChatModel struct {
	client        anthropic.Client
	config        providers.ModelConfig
	compatibility Compatibility
	options       *agentmodel.Options
}

func (model *ChatModel) InputEstimator() agentmodel.InputEstimator {
	return model.config.InputEstimator()
}

var (
	_ providers.ProtocolAdapter       = (*Adapter)(nil)
	_ agentmodel.ToolCallingChatModel = (*ChatModel)(nil)
)

func (*Adapter) New(_ context.Context, config providers.ModelConfig) (agentmodel.ToolCallingChatModel, error) {
	if config.Protocol != providers.ProtocolAnthropicMessages {
		return nil, fmt.Errorf("anthropic messages: protocol must be %q", providers.ProtocolAnthropicMessages)
	}
	if strings.TrimSpace(string(config.Provider)) == "" {
		return nil, fmt.Errorf("anthropic messages: provider is required")
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("anthropic messages: model is required")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, fmt.Errorf("anthropic messages: base URL is required")
	}
	cloned, err := config.Clone()
	if err != nil {
		return nil, fmt.Errorf("anthropic messages config: %w", err)
	}
	compatibility, err := resolveCompatibility(cloned)
	if err != nil {
		return nil, err
	}
	return &ChatModel{
		client:        anthropic.NewClient(anthropicClientOptions(cloned)...),
		config:        cloned,
		compatibility: compatibility,
		options:       &agentmodel.Options{},
	}, nil
}

func anthropicClientOptions(config providers.ModelConfig) []option.RequestOption {
	result := []option.RequestOption{
		option.WithMaxRetries(0),
		option.WithAPIKey(config.APIKey),
		option.WithBaseURL(config.BaseURL),
		option.WithHTTPClient(wrapAnthropicHTTPClient(llmhttp.Client(config.HTTPClient))),
	}
	names := make([]string, 0, len(config.Headers))
	for name := range config.Headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, option.WithHeader(name, config.Headers[name]))
	}
	return result
}

const maxDirectJSONMessageBytes = 32 << 20

type jsonFallbackKey struct{}

type jsonMessageFallback struct {
	captured bool
	body     []byte
	readErr  error
}

func withJSONMessageFallback(ctx context.Context) (context.Context, *jsonMessageFallback) {
	if ctx == nil {
		ctx = context.Background()
	}
	holder := &jsonMessageFallback{}
	return context.WithValue(ctx, jsonFallbackKey{}, holder), holder
}

func wrapAnthropicHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	clone := *client
	if _, ok := clone.Transport.(*anthropicJSONFallbackTransport); ok {
		return &clone
	}
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &anthropicJSONFallbackTransport{base: base}
	return &clone
}

type anthropicJSONFallbackTransport struct {
	base http.RoundTripper
}

func (t *anthropicJSONFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	holder, ok := req.Context().Value(jsonFallbackKey{}).(*jsonMessageFallback)
	if !ok || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if !strings.Contains(contentType, "application/json") || strings.Contains(contentType, "text/event-stream") {
		return resp, nil
	}
	// Some Anthropic-compatible gateways respond to stream=true with a single
	// application/json Message payload instead of text/event-stream.
	holder.captured = true
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxDirectJSONMessageBytes+1))
	_ = resp.Body.Close()
	if readErr != nil {
		holder.readErr = readErr
	} else if len(data) > maxDirectJSONMessageBytes {
		holder.readErr = fmt.Errorf("anthropic messages JSON response exceeds %d bytes", maxDirectJSONMessageBytes)
	} else {
		holder.body = data
	}
	resp.Body = http.NoBody
	return resp, nil
}
