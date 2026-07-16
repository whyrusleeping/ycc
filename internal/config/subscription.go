package config

import (
	"fmt"
	"sort"

	"github.com/whyrusleeping/ycc/internal/codex"
)

// EnsureSubscriptionModel makes sure c contains a model entry that uses
// subscription (auth = "oauth") credentials for the given backend ("anthropic"
// or "openai"), so a fresh `ycc login <backend>` is immediately usable without
// hand-editing ycc.toml (spec §13). If a subscription model for that backend
// already exists it is returned unchanged (added=false). Otherwise a new
// logical model is added under a sensible free name ("claude"/"chatgpt",
// falling back to a "-oauth" suffix when taken) with the backend's curated
// default model id. Empty roles are pointed at the new model so a freshly
// created config validates; populated roles are never touched.
func EnsureSubscriptionModel(c *Config, backend string) (name string, added bool, err error) {
	var defName, baseURL, modelID string
	switch backend {
	case "anthropic":
		defName = "claude"
		baseURL = "https://api.anthropic.com"
		if ids := CuratedModelIDs("anthropic"); len(ids) > 0 {
			modelID = ids[0]
		}
	case "openai":
		// ChatGPT subscription inference goes through the codex transport;
		// an empty base_url resolves to the codex backend at Build time.
		defName = "chatgpt"
		modelID = codex.Models[0]
	default:
		return "", false, fmt.Errorf("subscription auth is not supported for backend %q", backend)
	}

	// Already configured? Return the existing entry (deterministically: first
	// matching name in sorted order) and change nothing.
	names := make([]string, 0, len(c.Models))
	for n := range c.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if m := c.Models[n]; m.Backend == backend && m.Auth == "oauth" {
			return n, false, nil
		}
	}

	name = defName
	if _, taken := c.Models[name]; taken {
		name = defName + "-oauth"
		for i := 2; ; i++ {
			if _, taken := c.Models[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s-oauth%d", defName, i)
		}
	}
	if c.Models == nil {
		c.Models = make(map[string]Model)
	}
	c.Models[name] = Model{Backend: backend, BaseURL: baseURL, Model: modelID, Auth: "oauth"}
	// A freshly created config has no roles yet: point them at the new model
	// so the result passes validation. Existing role assignments are kept.
	if c.Roles.Coordinator == "" {
		c.Roles.Coordinator = name
	}
	if c.Roles.Implementer == "" {
		c.Roles.Implementer = name
	}
	if len(c.Roles.Reviewers) == 0 {
		c.Roles.Reviewers = []string{name}
	}
	return name, true, nil
}
