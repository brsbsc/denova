// Package protocolutil holds small request and error helpers shared across
// built-in model protocol adapters.
package protocolutil

import (
	"context"
	"strings"

	agentschema "github.com/alfredxw/denova/agent/schema"
)

var jsonPathKeyReplacer = strings.NewReplacer(
	`\`, `\\`,
	`.`, `\.`,
	`:`, `\:`,
	`#`, `\#`,
	`@`, `\@`,
)

// FilterTools retains only non-nil tool definitions whose names appear in names.
func FilterTools(tools []*agentschema.ToolInfo, names []string) []*agentschema.ToolInfo {
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	result := make([]*agentschema.ToolInfo, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			if _, ok := allowed[tool.Name]; ok {
				result = append(result, tool)
			}
		}
	}
	return result
}

// EscapeJSONPathKey escapes special sjson path characters in an object key.
func EscapeJSONPathKey(key string) string {
	return jsonPathKeyReplacer.Replace(key)
}

// ModelCallError preserves caller context cancellation before adapting a
// vendor SDK error.
func ModelCallError(ctx context.Context, err error, adapt func(error) error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if adapt != nil {
		return adapt(err)
	}
	return err
}
