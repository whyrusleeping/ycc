package orchestrator

import (
	"fmt"
	"strings"
	"testing"
)

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
