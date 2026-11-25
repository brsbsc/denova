package automationapp

import (
	agentexecution "denova/internal/agents/execution"
	"path/filepath"
	"strings"

	"denova/config"
	"denova/internal/agents/session"
	"denova/internal/book"
	projectdomain "denova/internal/project"
)

// automationWorkspaceSnapshot binds asynchronous trigger evaluation and any
// resulting automation run to one workspace runtime. The Host keeps the
// referenced generation alive with an Operation for as long as it is used.
type automationWorkspaceSnapshot struct {
	projectID        string
	projectType      projectdomain.Type
	stateRoot        string
	workspace        string
	novaDir          string
	cfg              config.Config
	bookState        *book.State
	bookService      *book.Service
	sessionStore     *session.Store
	executionRuntime *agentexecution.Runtime
}

func canonicalAutomationWorkspace(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return filepath.Clean(workspace)
	}
	if canonical, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(canonical)
	}
	return filepath.Clean(abs)
}
