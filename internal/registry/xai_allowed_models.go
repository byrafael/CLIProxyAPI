package registry

import (
	"strings"
)

func xaiBareModelName(modelID string) string {
	name := strings.ToLower(strings.TrimSpace(modelID))
	if lastOpen := strings.LastIndex(name, "("); lastOpen >= 0 && strings.HasSuffix(name, ")") {
		name = strings.TrimSpace(name[:lastOpen])
	}
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// IsXAICatalogModelID reports whether a model may appear on xAI/Grok credentials.
// The published catalog is grok-* and composer-* only.
func IsXAICatalogModelID(modelID string) bool {
	name := xaiBareModelName(modelID)
	if name == "" {
		return false
	}
	return strings.HasPrefix(name, "grok-") || strings.HasPrefix(name, "composer-")
}

// IsXAIForeignModelID reports Claude and OpenAI model names that must not run
// on xAI/Grok credentials even when a client sends them directly.
func IsXAIForeignModelID(modelID string) bool {
	name := xaiBareModelName(modelID)
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, "claude-") {
		return true
	}
	if strings.HasPrefix(name, "gpt-") || strings.HasPrefix(name, "chatgpt-") {
		return true
	}
	if name == "o1" || strings.HasPrefix(name, "o1-") {
		return true
	}
	if name == "o3" || strings.HasPrefix(name, "o3-") {
		return true
	}
	if name == "o4" || strings.HasPrefix(name, "o4-") {
		return true
	}
	return false
}

// FilterXAIAllowedModels keeps catalog entries whose IDs are grok-* or composer-*.
func FilterXAIAllowedModels(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return models
	}
	filtered := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		if IsXAICatalogModelID(model.ID) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}
