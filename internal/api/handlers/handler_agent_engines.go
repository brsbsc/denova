package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"

	"denova/config"
	"denova/internal/agents/conversationconfig"
	agentruntime "denova/internal/agents/runtime"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func (h *Handlers) HandleAgentEngines(_ context.Context, c *app.RequestContext) {
	writeJSON(c, consts.StatusOK, map[string]any{"items": h.app.AgentEngines().Catalog()})
}

func (h *Handlers) HandleAgentEngineCheck(ctx context.Context, c *app.RequestContext) {
	var body struct{}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	result, err := h.app.AgentEngines().Check(ctx, config.RuntimeID(c.Param("id")))
	if err != nil {
		writeEngineError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, result)
}

func (h *Handlers) HandleAgentEngineModels(ctx context.Context, c *app.RequestContext) {
	result, err := h.app.AgentEngines().Models(ctx, config.RuntimeID(c.Param("id")))
	if err != nil {
		writeEngineError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, result)
}

func (h *Handlers) HandleAgentEngineCLICheckUpdate(ctx context.Context, c *app.RequestContext) {
	var body struct{}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineCLIError(ctx, c, err)
		return
	}
	result, err := engines.CLI().CheckUpdate(ctx, id)
	if err != nil {
		writeEngineCLIError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, result)
}

func (h *Handlers) HandleAgentEngineCLIInstall(ctx context.Context, c *app.RequestContext) {
	var body struct{}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineCLIError(ctx, c, err)
		return
	}
	result, err := engines.CLI().StartInstall(id)
	if err != nil {
		writeEngineCLIError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusAccepted, result)
}

func (h *Handlers) HandleAgentEngineCLIStatus(ctx context.Context, c *app.RequestContext) {
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	desc, err := engines.Descriptor(id)
	if err != nil {
		writeEngineCLIError(ctx, c, err)
		return
	}
	managed := engines.CLI().Status(id)
	if managed == nil {
		writeEngineCLIError(ctx, c, agentruntime.ErrCLIManagementUnsupported)
		return
	}
	writeJSON(c, consts.StatusOK, map[string]any{
		"managed": managed,
		"engine":  desc,
	})
}

func (h *Handlers) HandleAgentEngineAuthStart(ctx context.Context, c *app.RequestContext) {
	var body agentruntime.AuthStartOptions
	if raw := bytes.TrimSpace(c.Request.Body()); len(raw) > 0 {
		if err := decodeStrictJSONRequest(raw, &body); err != nil {
			writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
			return
		}
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	snap, err := engines.Auth().Start(ctx, id, body)
	if err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, snap)
}

func (h *Handlers) HandleAgentEngineAuthStatus(ctx context.Context, c *app.RequestContext) {
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	snap, err := engines.Auth().Get(id, strings.TrimSpace(c.Param("session_id")))
	if err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, snap)
}

func (h *Handlers) HandleAgentEngineAuthSubmit(ctx context.Context, c *app.RequestContext) {
	var body struct {
		Input string `json:"input"`
	}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	snap, err := engines.Auth().Submit(ctx, id, strings.TrimSpace(c.Param("session_id")), body.Input)
	if err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, snap)
}

func (h *Handlers) HandleAgentEngineAuthCancel(ctx context.Context, c *app.RequestContext) {
	var body struct{}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	snap, err := engines.Auth().Cancel(id, strings.TrimSpace(c.Param("session_id")))
	if err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, snap)
}

func (h *Handlers) HandleAgentEngineAuthLogout(ctx context.Context, c *app.RequestContext) {
	var body struct{}
	if err := decodeStrictJSONRequest(c.Request.Body(), &body); err != nil {
		writeErrorKey(c, consts.StatusBadRequest, "api.common.invalidRequest")
		return
	}
	id := config.RuntimeID(c.Param("id"))
	engines := h.app.AgentEngines()
	if _, err := engines.Descriptor(id); err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	desc, err := engines.Auth().Logout(ctx, id)
	if err != nil {
		writeEngineAuthError(ctx, c, err)
		return
	}
	writeJSON(c, consts.StatusOK, desc)
}

func writeEngineError(ctx context.Context, c *app.RequestContext, err error) {
	status, key := consts.StatusServiceUnavailable, "agentRuntime.connectionFailed"
	switch {
	case errors.Is(err, agentruntime.ErrEngineNotFound):
		status, key = consts.StatusNotFound, "agentRuntime.notFound"
	case errors.Is(err, conversationconfig.ErrRuntimeCapabilityUnsupported):
		status, key = consts.StatusBadRequest, "agentRuntime.capabilityUnsupported"
	case errors.Is(err, agentruntime.ErrOperationActive):
		status, key = consts.StatusConflict, "agentRuntime.busy"
	case errors.Is(err, agentruntime.ErrEngineModelUnavailable):
		status, key = consts.StatusUnprocessableEntity, "agentRuntime.modelUnavailable"
	case errors.Is(err, agentruntime.ErrEngineNotReady):
		status, key = consts.StatusConflict, "agentRuntime.notReady"
	case errors.Is(err, agentruntime.ErrEngineNotInstalled):
		key = "agentRuntime.notInstalled"
	case agentruntime.VersionUnsupportedReasonKey(err) != "":
		key = agentruntime.VersionUnsupportedReasonKey(err)
	}
	// Do not log raw authentication errors or URLs, which can contain secrets.
	slog.WarnContext(ctx, "Agent runtime API operation failed", "runtime", c.Param("id"), "reason", key)
	writeErrorKey(c, status, key, "detail", err.Error())
}

func writeEngineCLIError(ctx context.Context, c *app.RequestContext, err error) {
	status, key := consts.StatusServiceUnavailable, "agentRuntime.connectionFailed"
	switch {
	case errors.Is(err, agentruntime.ErrEngineNotFound):
		status, key = consts.StatusNotFound, "agentRuntime.notFound"
	case errors.Is(err, conversationconfig.ErrRuntimeCapabilityUnsupported),
		errors.Is(err, agentruntime.ErrCLIManagementUnsupported):
		status, key = consts.StatusBadRequest, "agentRuntime.capabilityUnsupported"
	case errors.Is(err, agentruntime.ErrCLIOperationBusy):
		status, key = consts.StatusConflict, "agentRuntime.updateBusy"
	case errors.Is(err, agentruntime.ErrOperationActive):
		status, key = consts.StatusConflict, "agentRuntime.busy"
	}
	// Omit raw upstream network/URL diagnostics from the HTTP response body.
	slog.WarnContext(ctx, "Agent runtime CLI operation failed", "runtime", c.Param("id"), "reason", key)
	writeErrorKey(c, status, key)
}

func writeEngineAuthError(ctx context.Context, c *app.RequestContext, err error) {
	status, key := consts.StatusServiceUnavailable, "agentRuntime.auth.failed"
	switch {
	case errors.Is(err, agentruntime.ErrEngineNotFound):
		status, key = consts.StatusNotFound, "agentRuntime.notFound"
	case errors.Is(err, agentruntime.ErrAuthUnsupported),
		errors.Is(err, conversationconfig.ErrRuntimeCapabilityUnsupported):
		status, key = consts.StatusBadRequest, "agentRuntime.capabilityUnsupported"
	case errors.Is(err, agentruntime.ErrAuthSessionNotFound):
		status, key = consts.StatusNotFound, "agentRuntime.auth.expired"
	case errors.Is(err, agentruntime.ErrAuthSessionInvalidState):
		status, key = consts.StatusConflict, "agentRuntime.auth.failed"
	case errors.Is(err, agentruntime.ErrAuthInputInvalid):
		status, key = consts.StatusBadRequest, "agentRuntime.auth.invalidInput"
	case errors.Is(err, agentruntime.ErrEngineNotInstalled):
		status, key = consts.StatusConflict, "agentRuntime.auth.notInstalled"
	case errors.Is(err, agentruntime.ErrOperationActive):
		status, key = consts.StatusConflict, "agentRuntime.auth.busy"
	}
	// Never log or emit raw auth errors, URLs, or submitted codes.
	slog.WarnContext(ctx, "Agent runtime auth operation failed", "runtime", c.Param("id"), "reason", key)
	writeErrorKey(c, status, key)
}
