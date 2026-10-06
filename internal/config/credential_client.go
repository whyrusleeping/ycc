package config

import (
	"context"
	"errors"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/llmhttp"
)

// credentialClient sanitizes provider errors while preserving APIError metadata.
// OAuth's serialized per-turn Apply updates key when the bearer token rotates.
type credentialClient struct {
	*gollama.Client
	key string
}

func (c *credentialClient) redact(err error) error {
	var apiErr *gollama.APIError
	if errors.As(err, &apiErr) {
		apiErr.Body = llmhttp.Redact(apiErr.Body, c.key)
	}
	return llmhttp.RedactError(err, c.key)
}

func (c *credentialClient) TurnCtx(ctx context.Context, opts gollama.RequestOptions) (*gollama.ResponseMessageGenerate, error) {
	resp, err := c.Client.TurnCtx(ctx, opts)
	return resp, c.redact(err)
}

func (c *credentialClient) TurnStreamCtx(ctx context.Context, opts gollama.RequestOptions, onDelta func(string)) (*gollama.ResponseMessageGenerate, error) {
	resp, err := c.Client.TurnStreamCtx(ctx, opts, onDelta)
	return resp, c.redact(err)
}
