//go:build oauthsmoke

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/whyrusleeping/gollama"
)

func TestLiveAnthropicOAuthReservedPrefix(t *testing.T) {
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(filepath.Join(root, "ycc", "ycc.toml"))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry(cfg)
	client, model, err := reg.Build("claude")
	if err != nil {
		t.Fatal(err)
	}
	stream, ok := client.(interface {
		TurnStream(gollama.RequestOptions, func(string)) (*gollama.ResponseMessageGenerate, error)
	})
	if !ok {
		t.Fatalf("OAuth client %T does not stream", client)
	}
	var live string
	resp, err := stream.TurnStream(gollama.RequestOptions{
		Model:    model,
		System:   "You are ycc, a coding assistant. Reply exactly as requested.",
		Messages: []gollama.Message{{Role: "user", Content: "Reply exactly OK."}},
		Options:  &gollama.Options{MaxTokens: 32},
	}, func(text string) { live = text })
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		t.Fatalf("response = %+v", resp)
	}
	if live == "" || live != resp.Choices[0].Message.Content {
		t.Fatalf("last streamed snapshot = %q, final = %q", live, resp.Choices[0].Message.Content)
	}
}
