package session

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/whyrusleeping/ycc/internal/config"
)

func TestAgentModelCatalogTracksLiveAvailabilityAndReasoning(t *testing.T) {
	s, _ := newTestSession(t)
	model, _ := s.reg.GetModel("b")
	model.Model = "model-a" // two logical names may share one configured identity
	model.Notes = "Operator advice"
	model.Modalities = []string{"text"}
	model.ContextWindow = 8192
	model.KeyEnv = "SECRET_ENV_REF"
	model.BaseURL = "https://host.invalid/?token=endpoint-secret"
	price := 2.0
	model.PriceInput = &price
	if err := s.reg.UpsertModel("b", model, false); err != nil {
		t.Fatal(err)
	}
	s.thinkLevels = map[string]string{"b": "off"}
	entries := s.agentModels()
	if len(entries) != 3 || entries[0].Name != "a" || entries[1].Name != "b" || entries[0].Model != entries[1].Model {
		t.Fatalf("unexpected catalog entries: %+v", entries)
	}
	b := entries[1]
	if b.Thinking != "" || b.Notes != "Operator advice" || len(b.Modalities) != 1 || b.ContextWindow != 8192 || !b.Priced || !b.PriceInputKnown || b.PriceOutputKnown {
		t.Fatalf("catalog did not resolve model capabilities: %+v", b)
	}
	for _, secret := range []string{"SECRET_ENV_REF", "endpoint-secret"} {
		if strings.Contains(fmt.Sprint(entries), secret) {
			t.Fatalf("catalog leaked %q: %+v", secret, entries)
		}
	}
	model.Disabled = true
	if err := s.reg.UpsertModel("b", model, false); err != nil {
		t.Fatal(err)
	}
	if entries := s.agentModels(); len(entries) != 2 || entries[0].Name != "a" || entries[1].Name != "c" {
		t.Fatalf("disabled model remained in catalog: %+v", entries)
	}
	if _, err := s.agentSpec("b"); !errors.Is(err, config.ErrModelDisabled) {
		t.Fatalf("disabled model spawn validation = %v", err)
	}
}
