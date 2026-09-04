package config

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/engine"
)

const modelProbeMaxTokens = 64

// ProbeModel performs one small inference request using a draft model record.
// It deliberately builds an isolated registry so testing a form does not mutate
// the live model registry or persist the draft. Disabled is ignored for the
// explicit diagnostic action; every other provider-facing setting follows the
// same Build and ResolveThinking paths as a normal turn.
func ProbeModel(ctx context.Context, name string, model Model) (time.Duration, error) {
	if err := model.Validate(name); err != nil {
		return 0, err
	}

	started := time.Now()
	credential := resolveKey(model)
	model.Disabled = false
	registry := NewRegistry(&Config{Models: map[string]Model{name: model}})
	turner, modelID, err := registry.BuildContext(ctx, name)
	if err != nil {
		return time.Since(started), redactProbeCredential(err, credential)
	}

	thinking := model.ResolveThinking()
	opts := gollama.RequestOptions{
		Model:           modelID,
		System:          "You are responding to a model configuration test.",
		Messages:        []gollama.Message{{Role: "user", Content: "Reply with OK."}},
		Thinking:        thinking.Thinking,
		Effort:          thinking.Effort,
		ThinkingDisplay: thinking.ThinkingDisplay,
		Options:         &gollama.Options{MaxTokens: modelProbeMaxTokens},
	}

	var response *gollama.ResponseMessageGenerate
	if streamer, ok := turner.(engine.StreamTurner); ok {
		response, err = streamer.TurnStreamCtx(ctx, opts, func(string) {})
	} else {
		response, err = turner.TurnCtx(ctx, opts)
	}
	duration := time.Since(started)
	if err != nil {
		// Preserve context cancellation/deadline identity for the RPC layer. Provider
		// and transport errors are flattened only after removing an exact credential
		// should a broken endpoint reflect its Authorization value in an error body.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return duration, ctxErr
		}
		return duration, redactProbeCredential(err, credential)
	}
	if response == nil || len(response.Choices) == 0 {
		return duration, errors.New("provider returned no completion choices")
	}
	return duration, nil
}

func redactProbeCredential(err error, credential string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if credential != "" {
		message = strings.ReplaceAll(message, credential, "[REDACTED]")
	}
	// Provider errors can contain an HTML gateway page or an echoed request. Keep
	// the internal diagnostic bounded even though the RPC exposes only a categorical
	// message derived from it.
	const maxRunes = 2_000
	if runes := []rune(message); len(runes) > maxRunes {
		message = string(runes[:maxRunes]) + "…"
	}
	return errors.New(message)
}
