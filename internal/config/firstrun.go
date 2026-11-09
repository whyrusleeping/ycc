package config

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"reflect"

	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
)

// FallbackModelName is the logical name of the single model in the built-in
// no-config fallback (DefaultAnthropic).
const FallbackModelName = "claude"

// MissingCredential names the credential a model needs but cannot find on this
// machine ("" when one is present or the model needs none). It only inspects
// local state, so callers can say "not set up" instead of sending a request
// that is certain to fail authentication.
func MissingCredential(m Model) string {
	switch {
	case m.Auth == "oauth" && m.Backend == "anthropic":
		if _, ok := anthropicauth.Load(); !ok {
			return "no Claude subscription login"
		}
	case m.Auth == "oauth" && m.Backend == "openai":
		if _, ok := openaiauth.Load(); !ok {
			return "no ChatGPT subscription login"
		}
	case m.Auth != "oauth" && m.KeyEnv != "" && resolveKey(m) == "":
		return "no API key in " + m.KeyEnv
	}
	return ""
}

// MarkBuiltinFallback records that the registry holds the built-in fallback
// rather than a loaded ycc.toml: nobody has chosen a model yet.
func (r *Registry) MarkBuiltinFallback() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = true
	r.fallbackModel = r.cfg.Models[FallbackModelName]
}

// AdoptAnthropicLogin turns a just-stored Claude subscription login into a
// working setup when no model has been set up yet: if the registry still holds
// the untouched built-in fallback and its model has no API key, that model
// switches to auth = "oauth" (keeping its model id and roles) and the config is
// persisted, as `ycc login anthropic` does on a machine without a config. Any
// configuration someone chose — settings edited through this daemon, or a
// ycc.toml written since startup (e.g. by `ycc login` in a shell) — is left
// alone. It reports whether it changed anything.
func (r *Registry) AdoptAnthropicLogin() (bool, error) {
	return r.adoptSubscriptionLogin("anthropic")
}

// AdoptOpenAILogin replaces the untouched, keyless built-in fallback with a
// ChatGPT subscription model and points all roles at it. The same guards as
// AdoptAnthropicLogin preserve any configuration the user has chosen.
func (r *Registry) AdoptOpenAILogin() (bool, error) {
	return r.adoptSubscriptionLogin("openai")
}

func (r *Registry) adoptSubscriptionLogin(backend string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.fallback || r.edited {
		return false, nil
	}
	if _, err := os.Stat(r.path); r.path == "" || !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	prev, ok := r.cfg.Models[FallbackModelName]
	if !ok || !reflect.DeepEqual(prev, r.fallbackModel) || prev.Backend != "anthropic" || prev.Auth == "oauth" || resolveKey(prev) != "" {
		return false, nil
	}
	previous := r.cfg
	next := *previous
	next.Models = maps.Clone(previous.Models)
	if backend == "anthropic" {
		m := prev
		m.Auth, m.KeyEnv = "oauth", ""
		next.Models[FallbackModelName] = m
	} else {
		delete(next.Models, FallbackModelName)
		name, _, err := EnsureSubscriptionModel(&next, backend)
		if err != nil {
			return false, err
		}
		next.Roles.Coordinator, next.Roles.Implementer = name, name
		next.Roles.Reviewers = []string{name}
	}
	r.cfg = &next
	if err := r.persistLocked(); err != nil {
		r.cfg = previous
		return false, err
	}
	r.fallback = false
	return true, nil
}
