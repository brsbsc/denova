// Package character imports third-party character cards into Denova's native
// lore and interactive-opening formats.
package character

// Service owns one workspace-scoped character import transaction boundary.
type Service struct {
	workspace string
}

// NewService creates a character-card importer for one workspace.
func NewService(workspace string) *Service {
	return &Service{workspace: workspace}
}

func truncateCardRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}
