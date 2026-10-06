package config

import (
	"testing"

	"github.com/whyrusleeping/ycc/internal/credenv"
)

func TestRegistryRegistersCredentialEnvironment(t *testing.T) {
	registry := NewRegistry(DefaultAnthropic("", "claude", "CONFIG_TEST_INITIAL_KEY", 0))
	if got := credenv.Scrub([]string{"CONFIG_TEST_INITIAL_KEY=secret"}); len(got) != 0 {
		t.Fatalf("initial model key inherited: %v", got)
	}
	if err := registry.UpsertModel("custom", Model{Backend: "openai", Model: "test", KeyEnv: "CONFIG_TEST_RUNTIME_KEY"}, false); err != nil {
		t.Fatal(err)
	}
	if err := registry.RemoveModel("custom", false); err != nil {
		t.Fatal(err)
	}
	if got := credenv.Scrub([]string{"CONFIG_TEST_RUNTIME_KEY=secret"}); len(got) != 0 {
		t.Fatalf("retired model key inherited: %v", got)
	}
}
