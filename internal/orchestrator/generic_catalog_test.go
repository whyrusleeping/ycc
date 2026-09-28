package orchestrator

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestGenericModelCatalog(t *testing.T) {
	models := []ModelCatalogEntry{
		{Name: "fast", Backend: "openai", Model: "shared-id", Auth: "oauth", ContextWindow: 200000,
			Thinking: "adaptive", Effort: "high", Priced: true, PriceInput: 1, PriceOutput: 5,
			PriceInputKnown: true, PriceOutputKnown: true, Modalities: []string{"text", "image"},
			Notes: "Good for\n quick  inspection"},
		{Name: "fallback", Backend: "openai", Model: "shared-id"},
	}
	d := &Deps{AgentModels: func() []ModelCatalogEntry { return models }}
	prop := genericModelProp(d)
	if names, ok := prop["enum"].([]string); !ok || !slices.Equal(names, []string{"fast", "fallback"}) {
		t.Fatalf("model enum = %v", prop["enum"])
	}
	desc := prop["description"].(string)
	for _, want := range []string{
		"fast: openai/shared-id (subscription)", "ctx 200000", "reasoning adaptive/high", "$1/$5 per Mtok in/out",
		"modalities text,image", `operator note: "Good for quick inspection"`,
		"fallback: openai/shared-id", "ctx unknown", "reasoning off", "price unknown", "modalities unknown",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("catalog missing %q: %s", want, desc)
		}
	}
}

func TestGenericModelCatalogBounded(t *testing.T) {
	models := make([]ModelCatalogEntry, 40)
	for i := range models {
		models[i] = ModelCatalogEntry{
			Name: fmt.Sprintf("model-%02d", i), Backend: "openai", Model: "custom-id",
			Notes: strings.Repeat("very long advice ", 100),
		}
	}
	descProp := genericModelProp(&Deps{AgentModels: func() []ModelCatalogEntry { return models }})
	if names := descProp["enum"].([]string); len(names) != len(models) || names[39] != "model-39" {
		t.Fatalf("bounded catalog lost selectable names: %v", names)
	}
	desc := descProp["description"].(string)
	if len(desc) > 2500 || !strings.Contains(desc, "more (details omitted; names in enum)") || strings.Contains(desc, "model-39:") {
		t.Fatalf("catalog not bounded (%d bytes): %s", len(desc), desc)
	}
	if strings.Contains(desc, strings.Repeat("very long advice ", 12)) {
		t.Fatalf("operator note not truncated: %s", desc)
	}
}
