package registry

import "testing"

func TestIsXAICatalogModelID(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"grok-4.7",
		"grok-4.7(high)",
		"xai/grok-4.7",
		"grok-composer-2.5-fast",
		"composer-2.5",
		"COMPOSER-1",
		"grok-imagine-image",
		"grok-build-0.1",
	}
	for _, model := range allowed {
		if !IsXAICatalogModelID(model) {
			t.Fatalf("IsXAICatalogModelID(%q) = false, want true", model)
		}
	}

	blocked := []string{
		"",
		"claude-sonnet-4-5-20250929",
		"gpt-5.6",
		"gemini-2.5-pro",
		"test",
	}
	for _, model := range blocked {
		if IsXAICatalogModelID(model) {
			t.Fatalf("IsXAICatalogModelID(%q) = true, want false", model)
		}
	}
}

func TestIsXAIForeignModelID(t *testing.T) {
	t.Parallel()

	foreign := []string{
		"claude-sonnet-4-5-20250929",
		"claude-opus-4-6",
		"gpt-5.6",
		"gpt-4o",
		"chatgpt-4o-latest",
		"o1",
		"o1-preview",
		"o3-mini",
		"o4-mini",
		"xai/gpt-5.6",
		"gpt-5.6(high)",
	}
	for _, model := range foreign {
		if !IsXAIForeignModelID(model) {
			t.Fatalf("IsXAIForeignModelID(%q) = false, want true", model)
		}
	}

	native := []string{
		"",
		"grok-4.7",
		"composer-2.5",
		"grok-composer-2.5-fast",
		"test",
		"task6-public-patch",
		"gemini-2.5-pro",
	}
	for _, model := range native {
		if IsXAIForeignModelID(model) {
			t.Fatalf("IsXAIForeignModelID(%q) = true, want false", model)
		}
	}
}

func TestFilterXAIAllowedModels(t *testing.T) {
	t.Parallel()

	got := FilterXAIAllowedModels([]*ModelInfo{
		{ID: "grok-4.7"},
		{ID: "claude-sonnet-4-5-20250929"},
		{ID: "gpt-5.6"},
		{ID: "composer-2"},
		nil,
		{ID: "grok-composer-2.5-fast"},
	})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %#v", len(got), got)
	}
	if got[0].ID != "grok-4.7" || got[1].ID != "composer-2" || got[2].ID != "grok-composer-2.5-fast" {
		t.Fatalf("filtered IDs = [%s, %s, %s]", got[0].ID, got[1].ID, got[2].ID)
	}
}
