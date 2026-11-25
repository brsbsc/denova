// Package compat contains compatibility repairs for non-standard providers
// that expose an OpenAI-compatible Chat Completions endpoint.
package compat

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"

	agentmodel "github.com/alfredxw/denova/agent/model"
	agentstream "github.com/alfredxw/denova/agent/model/stream"
	agentschema "github.com/alfredxw/denova/agent/schema"
)

// Config contains only the endpoint traits needed by compatibility repairs.
type Config struct {
	Model                 string
	RepairTextToolCalls   bool
	RepairInlineThinking  bool
	RequestReasoningSplit bool
}

// Wrap returns a possibly-decorated chat model that hides provider-specific
// quirks. If the model needs no polyfill, the original is returned untouched.
func Wrap(cm agentmodel.ToolCallingChatModel, cfg Config) agentmodel.ToolCallingChatModel {
	if polyfills := detect(cfg); len(polyfills) > 0 {
		slog.InfoContext(context.Background(), fmt.Sprintf("[providercompat] Wrap called: applying %d polyfill(s) model=%q", len(polyfills), cfg.Model))
		cm = chain(cm, polyfills)
	}
	return cm
}

// ExtraRequestFields returns provider-specific fields that should be merged
// into the request body (e.g. reasoning_split to ask the API to return
// thinking via the standard reasoning_content field). Called once when
// building the chat model config, before any request is sent.
func ExtraRequestFields(cfg Config) map[string]any {
	out := map[string]any{}
	if cfg.RequestReasoningSplit {
		// Ask the provider to return thinking via the standard
		// reasoning_content field, instead of embedding it in content.
		out["reasoning_split"] = true
	}
	return out
}

type polyfill interface {
	apply(agentmodel.ToolCallingChatModel) agentmodel.ToolCallingChatModel
}

// detect inspects the config and returns the polyfill chain to apply.
// Order matters: later polyfills see output of earlier ones.
func detect(cfg Config) []polyfill {
	var out []polyfill
	if cfg.RepairTextToolCalls {
		out = append(out, toolCallTextPolyfill{})
	}
	if cfg.RepairInlineThinking {
		out = append(out, inlineThinkPolyfill{})
	}
	return out
}

func chain(cm agentmodel.ToolCallingChatModel, ps []polyfill) agentmodel.ToolCallingChatModel {
	for _, p := range ps {
		cm = p.apply(cm)
	}
	return cm
}

// -----------------------------------------------------------------------------
// Polyfill 1: tool calls delivered as inline text instead of structured
// tool_calls. We parse the antml-style <tool_call><invoke name="...">…</invoke> </tool_call>
// XML and promote them to agent.ToolCall so the framework actually executes
// the tools.
// -----------------------------------------------------------------------------

type toolCallTextPolyfill struct{}

var (
	pcInvokeRe    = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"\s*>(.*?)</invoke>`)
	pcToolCallRe  = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>`)
	pcParamNamedR = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"\s*>(.*?)</parameter>`)
	pcParamTagR   = regexp.MustCompile(`(?s)<([a-zA-Z_][\w.-]*)>(.*?)</[a-zA-Z_][\w.-]*>`)
)

func (toolCallTextPolyfill) apply(inner agentmodel.ToolCallingChatModel) agentmodel.ToolCallingChatModel {
	return &toolCallTextModel{inner: inner}
}

type toolCallTextModel struct {
	inner agentmodel.ToolCallingChatModel
}

func (m *toolCallTextModel) InputEstimator() agentmodel.InputEstimator {
	if model, ok := m.inner.(agentmodel.ModelInputEstimator); ok {
		return model.InputEstimator()
	}
	return agentmodel.InputEstimator{}
}

func (m *toolCallTextModel) Generate(ctx context.Context, in []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentschema.Message, error) {
	msg, err := m.inner.Generate(ctx, in, opts...)
	if err != nil || msg == nil {
		return msg, err
	}
	extractTextToolCalls(msg)
	return msg, nil
}

func (m *toolCallTextModel) Stream(ctx context.Context, in []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	sr, err := m.inner.Stream(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	repair := &textToolCallStreamRepair{}
	return transformMessageStream(sr, repair.push, repair.flush), nil
}

func (m *toolCallTextModel) WithTools(tools []*agentschema.ToolInfo) (agentmodel.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &toolCallTextModel{inner: inner}, nil
}

func extractTextToolCalls(msg *agentschema.Message) {
	extractTextToolCallsFrom(msg, 0)
}

func extractTextToolCallsFrom(msg *agentschema.Message, startIndex int) int {
	if msg == nil || len(msg.ToolCalls) > 0 || msg.Content == "" {
		return startIndex
	}
	matches := pcInvokeRe.FindAllStringSubmatch(msg.Content, -1)
	if len(matches) == 0 {
		return startIndex
	}
	calls := make([]agentschema.ToolCall, 0, len(matches))
	nextIndex := startIndex
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" {
			continue
		}
		params := parseInvokeParams(m[2])
		args, _ := json.Marshal(params)
		idx := nextIndex
		nextIndex++
		calls = append(calls, agentschema.ToolCall{
			Index: &idx,
			ID:    newTextToolCallID(),
			Type:  "function",
			Function: agentschema.FunctionCall{
				Name:      name,
				Arguments: string(args),
			},
		})
	}
	if len(calls) == 0 {
		return startIndex
	}
	msg.ToolCalls = calls
	msg.Content = pcToolCallRe.ReplaceAllString(msg.Content, "")
	msg.Content = pcInvokeRe.ReplaceAllString(msg.Content, "")
	return nextIndex
}

func newTextToolCallID() string {
	// The synthetic ID becomes part of the durable transcript. Randomness keeps
	// repeated textual calls distinct across turns, model instances, and process
	// restarts instead of resetting an in-memory counter on every response.
	return "text_tool_call_" + strings.ToLower(rand.Text())
}

func parseInvokeParams(body string) map[string]string {
	out := map[string]string{}
	if named := pcParamNamedR.FindAllStringSubmatch(body, -1); len(named) > 0 {
		for _, m := range named {
			if k := strings.TrimSpace(m[1]); k != "" {
				out[k] = strings.TrimSpace(m[2])
			}
		}
		return out
	}
	for _, m := range pcParamTagR.FindAllStringSubmatch(body, -1) {
		k := strings.TrimSpace(m[1])
		if k == "" || strings.EqualFold(k, "parameter") {
			continue
		}
		out[k] = strings.TrimSpace(m[2])
	}
	return out
}

// -----------------------------------------------------------------------------
// Polyfill 2: some providers (or fallback paths) still emit <think>…</think>
// inline. Strip them from content and surface as ReasoningContent if missing.
// -----------------------------------------------------------------------------

type inlineThinkPolyfill struct{}

func (inlineThinkPolyfill) apply(inner agentmodel.ToolCallingChatModel) agentmodel.ToolCallingChatModel {
	return &inlineThinkModel{inner: inner}
}

type inlineThinkModel struct {
	inner agentmodel.ToolCallingChatModel
}

func (m *inlineThinkModel) InputEstimator() agentmodel.InputEstimator {
	if model, ok := m.inner.(agentmodel.ModelInputEstimator); ok {
		return model.InputEstimator()
	}
	return agentmodel.InputEstimator{}
}

func (m *inlineThinkModel) Generate(ctx context.Context, in []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentschema.Message, error) {
	msg, err := m.inner.Generate(ctx, in, opts...)
	if err != nil || msg == nil {
		return msg, err
	}
	stripInlineThink(msg)
	return msg, nil
}

func (m *inlineThinkModel) Stream(ctx context.Context, in []*agentschema.Message, opts ...agentmodel.ModelOption) (*agentstream.StreamReader[*agentschema.Message], error) {
	sr, err := m.inner.Stream(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	repair := &inlineThinkStreamRepair{}
	return transformMessageStream(sr, repair.push, repair.flush), nil
}

func (m *inlineThinkModel) WithTools(tools []*agentschema.ToolInfo) (agentmodel.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &inlineThinkModel{inner: inner}, nil
}

func stripInlineThink(msg *agentschema.Message) {
	if msg == nil || msg.Content == "" {
		return
	}
	clean, thinking := stripThinkTagsSimple(msg.Content)
	if thinking != "" && strings.TrimSpace(msg.ReasoningContent) == "" {
		msg.ReasoningContent = thinking
	}
	msg.Content = clean
}

// stripThinkTagsSimple removes paired/unclosed <think>…</think> and orphan </think>
// prelude in one shot. Used on whole-message content (post-stream concat), so
// regex is fine — no cross-chunk state to maintain. The agent package's
// thinkTagExtractor handles the streaming variant separately.
func stripThinkTagsSimple(s string) (content, thinking string) {
	// paired <think>…</think> (lazy, may not find anything if unclosed)
	paired := regexp.MustCompile(`(?is)<think>(.*?)(?:</think>|$)`)
	matches := paired.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		// no <think> opener: maybe an orphan </think> prelude
		if idx := strings.Index(strings.ToLower(s), "</think>"); idx >= 0 {
			prelude := strings.TrimSpace(s[:idx])
			if prelude != "" {
				thinking = prelude
			}
			content = strings.TrimLeft(s[idx+len("</think>"):], " \t\r\n")
		} else {
			content = s
		}
		return content, thinking
	}
	var contentBuilder, thinkBuilder strings.Builder
	last := 0
	for _, m := range matches {
		if m[0] > last {
			contentBuilder.WriteString(s[last:m[0]])
		}
		thinkBuilder.WriteString(s[m[2]:m[3]])
		last = m[1]
	}
	contentBuilder.WriteString(s[last:])
	// also strip any orphan </think> remaining in the content tail
	content = paired.ReplaceAllString(contentBuilder.String(), "")
	// and any orphan </think> fragments
	content = regexp.MustCompile(`(?i)\n?</think>\s*`).ReplaceAllString(content, "")
	content = strings.TrimLeft(content, " \t\r\n")
	return content, thinkBuilder.String()
}

type streamEmitFunc func(*agentschema.Message)

func transformMessageStream(
	input *agentstream.StreamReader[*agentschema.Message],
	push func(*agentschema.Message, streamEmitFunc),
	flush func(streamEmitFunc),
) *agentstream.StreamReader[*agentschema.Message] {
	output, writer := agentstream.Pipe[*agentschema.Message](1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				writer.Send(nil, fmt.Errorf("providercompat stream transform panic: %v", recovered))
			}
			writer.Close()
			input.Close()
		}()
		emit := func(message *agentschema.Message) {
			if message == nil || emptyMessage(message) {
				return
			}
			writer.Send(message, nil)
		}
		for {
			frame, err := input.Recv()
			if errors.Is(err, io.EOF) {
				if flush != nil {
					flush(emit)
				}
				return
			}
			if err != nil {
				writer.Send(nil, err)
				return
			}
			if push != nil {
				push(frame, emit)
			} else {
				emit(frame)
			}
		}
	}()
	return output
}

func emptyMessage(message *agentschema.Message) bool {
	return message.Content == "" &&
		message.ReasoningContent == "" &&
		len(message.ToolCalls) == 0 &&
		len(message.MultiContent) == 0 &&
		len(message.UserInputMultiContent) == 0 &&
		len(message.AssistantGenMultiContent) == 0 &&
		len(message.Extra) == 0 &&
		message.ResponseMeta == nil
}

type textToolCallStreamRepair struct {
	buffer        string
	inToolCall    bool
	nextToolIndex int
}

func (r *textToolCallStreamRepair) push(frame *agentschema.Message, emit streamEmitFunc) {
	if frame == nil {
		return
	}
	if frame.Content == "" || len(frame.ToolCalls) > 0 {
		emit(frame.Clone())
		return
	}
	carrier, role := detachFrameMetadata(frame, true, false)
	emitWithCarrier := wrapCarrierEmit(emit, &carrier)
	r.buffer += frame.Content
	r.drain(role, emitWithCarrier)
	if carrier != nil && !emptyMessage(carrier) {
		emit(carrier)
	}
}

func (r *textToolCallStreamRepair) flush(emit streamEmitFunc) {
	if r.buffer == "" {
		return
	}
	r.emitToolAwareContent(agentschema.Assistant, r.buffer, emit)
	r.buffer = ""
	r.inToolCall = false
}

func (r *textToolCallStreamRepair) drain(role agentschema.RoleType, emit streamEmitFunc) {
	const openTag = "<tool_call>"
	const closeTag = "</tool_call>"
	for r.buffer != "" {
		if r.inToolCall {
			closeIndex := indexFold(r.buffer, closeTag)
			if closeIndex < 0 {
				return
			}
			end := closeIndex + len(closeTag)
			r.emitToolAwareContent(role, r.buffer[:end], emit)
			r.buffer = r.buffer[end:]
			r.inToolCall = false
			continue
		}

		openIndex := indexFold(r.buffer, openTag)
		if openIndex >= 0 {
			if openIndex > 0 {
				emit(&agentschema.Message{Role: role, Content: r.buffer[:openIndex]})
			}
			r.buffer = r.buffer[openIndex:]
			r.inToolCall = true
			continue
		}
		keep := partialTagSuffixLength(r.buffer, openTag)
		if keep > 0 {
			if len(r.buffer) > keep {
				emit(&agentschema.Message{Role: role, Content: r.buffer[:len(r.buffer)-keep]})
				r.buffer = r.buffer[len(r.buffer)-keep:]
			}
			return
		}
		emit(&agentschema.Message{Role: role, Content: r.buffer})
		r.buffer = ""
	}
}

func (r *textToolCallStreamRepair) emitToolAwareContent(role agentschema.RoleType, content string, emit streamEmitFunc) {
	if content == "" {
		return
	}
	message := &agentschema.Message{Role: role, Content: content}
	r.nextToolIndex = extractTextToolCallsFrom(message, r.nextToolIndex)
	emit(message)
}

type inlineThinkStreamRepair struct {
	buffer  string
	inThink bool
}

func (r *inlineThinkStreamRepair) push(frame *agentschema.Message, emit streamEmitFunc) {
	if frame == nil {
		return
	}
	if frame.Content == "" {
		emit(frame.Clone())
		return
	}
	carrier, role := detachFrameMetadata(frame, true, true)
	emitWithCarrier := wrapCarrierEmit(emit, &carrier)
	r.buffer += frame.Content
	r.drain(role, emitWithCarrier)
	if carrier != nil && !emptyMessage(carrier) {
		emit(carrier)
	}
}

func (r *inlineThinkStreamRepair) flush(emit streamEmitFunc) {
	if r.buffer == "" {
		return
	}
	if r.inThink {
		emit(&agentschema.Message{Role: agentschema.Assistant, ReasoningContent: r.buffer})
	} else {
		clean, thinking := stripThinkTagsSimple(r.buffer)
		if thinking != "" {
			emit(&agentschema.Message{Role: agentschema.Assistant, ReasoningContent: thinking})
		}
		if clean != "" {
			emit(&agentschema.Message{Role: agentschema.Assistant, Content: clean})
		}
	}
	r.buffer = ""
	r.inThink = false
}

func (r *inlineThinkStreamRepair) drain(role agentschema.RoleType, emit streamEmitFunc) {
	const openTag = "<think>"
	const closeTag = "</think>"
	for r.buffer != "" {
		if r.inThink {
			closeIndex := indexFold(r.buffer, closeTag)
			if closeIndex >= 0 {
				if closeIndex > 0 {
					emit(&agentschema.Message{Role: role, ReasoningContent: r.buffer[:closeIndex]})
				}
				r.buffer = strings.TrimLeft(r.buffer[closeIndex+len(closeTag):], " \t\r\n")
				r.inThink = false
				continue
			}
			keep := partialTagSuffixLength(r.buffer, closeTag)
			if keep > 0 {
				if len(r.buffer) > keep {
					emit(&agentschema.Message{Role: role, ReasoningContent: r.buffer[:len(r.buffer)-keep]})
					r.buffer = r.buffer[len(r.buffer)-keep:]
				}
				return
			}
			emit(&agentschema.Message{Role: role, ReasoningContent: r.buffer})
			r.buffer = ""
			return
		}

		openIndex := indexFold(r.buffer, openTag)
		closeIndex := indexFold(r.buffer, closeTag)
		if closeIndex >= 0 && (openIndex < 0 || closeIndex < openIndex) {
			if closeIndex > 0 {
				emit(&agentschema.Message{Role: role, ReasoningContent: r.buffer[:closeIndex]})
			}
			r.buffer = strings.TrimLeft(r.buffer[closeIndex+len(closeTag):], " \t\r\n")
			continue
		}
		if openIndex >= 0 {
			if openIndex > 0 {
				emit(&agentschema.Message{Role: role, Content: r.buffer[:openIndex]})
			}
			r.buffer = r.buffer[openIndex+len(openTag):]
			r.inThink = true
			continue
		}
		keep := partialTagSuffixLength(r.buffer, openTag, closeTag)
		if keep > 0 {
			if len(r.buffer) > keep {
				emit(&agentschema.Message{Role: role, Content: r.buffer[:len(r.buffer)-keep]})
				r.buffer = r.buffer[len(r.buffer)-keep:]
			}
			return
		}
		emit(&agentschema.Message{Role: role, Content: r.buffer})
		r.buffer = ""
	}
}

func detachFrameMetadata(frame *agentschema.Message, includeReasoning, includeToolCalls bool) (*agentschema.Message, agentschema.RoleType) {
	cloned := frame.Clone()
	role := cloned.Role
	if role == "" {
		role = agentschema.Assistant
	}
	cloned.Role = role
	cloned.Content = ""
	if !includeReasoning {
		cloned.ReasoningContent = ""
	}
	if !includeToolCalls {
		cloned.ToolCalls = nil
	}
	return cloned, role
}

func wrapCarrierEmit(emit streamEmitFunc, carrier **agentschema.Message) streamEmitFunc {
	return func(message *agentschema.Message) {
		if message == nil || emptyMessage(message) {
			return
		}
		if *carrier != nil {
			meta := *carrier
			*carrier = nil
			if message.Name == "" {
				message.Name = meta.Name
			}
			if meta.ReasoningContent != "" {
				message.ReasoningContent = meta.ReasoningContent + message.ReasoningContent
			}
			if len(meta.ToolCalls) > 0 && len(message.ToolCalls) == 0 {
				message.ToolCalls = meta.ToolCalls
			}
			message.MultiContent = append(meta.MultiContent, message.MultiContent...)
			message.UserInputMultiContent = append(meta.UserInputMultiContent, message.UserInputMultiContent...)
			message.AssistantGenMultiContent = append(meta.AssistantGenMultiContent, message.AssistantGenMultiContent...)
			if len(meta.Extra) > 0 {
				if len(message.Extra) == 0 {
					message.Extra = meta.Extra
				} else {
					for key, value := range meta.Extra {
						if _, exists := message.Extra[key]; !exists {
							message.Extra[key] = value
						}
					}
				}
			}
			if meta.ResponseMeta != nil && message.ResponseMeta == nil {
				message.ResponseMeta = meta.ResponseMeta
			}
		}
		emit(message)
	}
}

func indexFold(value, needle string) int {
	return strings.Index(strings.ToLower(value), strings.ToLower(needle))
}

func partialTagSuffixLength(value string, tags ...string) int {
	maxLength := 0
	for _, tag := range tags {
		limit := len(tag) - 1
		if len(value) < limit {
			limit = len(value)
		}
		for length := limit; length > 0; length-- {
			if strings.EqualFold(value[len(value)-length:], tag[:length]) {
				if length > maxLength {
					maxLength = length
				}
				break
			}
		}
	}
	return maxLength
}
