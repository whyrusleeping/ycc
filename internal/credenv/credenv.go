// Package credenv prevents accidental propagation of daemon credentials to agent
// subprocesses. It uses a denylist rather than an allowlist so operator tools
// (gh, ssh-agent, npm registries, Go proxies) retain their ordinary environment.
// This is not isolation: same-uid shells can still read credential files or
// inspect the daemon. Explicit workspace environment overrides remain trusted.
package credenv

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
)

var reserved = map[string]bool{
	"YCC_TOKEN": true, "ANTHROPIC_OAUTH": true, "OPENAI_OAUTH": true,
}

var credentials = struct {
	sync.RWMutex
	names map[string]bool
}{names: map[string]bool{
	"YCC_TOKEN": true, "ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true,
	"OPENAI_API_KEY": true, "EXA_API_KEY": true,
	"ANTHROPIC_OAUTH": true, "OPENAI_OAUTH": true,
}}

// Register adds configured provider key references to the process-wide scrub set.
// Names are never removed: retired configurations may still have live credentials.
func Register(names ...string) {
	credentials.Lock()
	defer credentials.Unlock()
	for _, name := range names {
		if name != "" {
			credentials.names[name] = true
		}
	}
}

// Scrub returns a new environment with known daemon/provider credentials removed.
func Scrub(env []string) []string {
	credentials.RLock()
	defer credentials.RUnlock()
	out := make([]string, 0, len(env))
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if !credentials.names[key] {
			out = append(out, item)
		}
	}
	return out
}

// ValidateKeyRef rejects daemon authentication and internal OAuth records as API keys.
func ValidateKeyRef(name string) error {
	if reserved[name] {
		return fmt.Errorf("key_env %q is reserved for internal ycc credentials", name)
	}
	return nil
}

// ValidateCredentialURL requires TLS for credential-bearing custom endpoints,
// except HTTP loopback endpoints used by local providers and development servers.
func ValidateCredentialURL(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	u, err := url.Parse(baseURL)
	if err == nil && u.Hostname() != "" {
		if u.Scheme == "https" {
			return nil
		}
		if u.Scheme == "http" {
			host := u.Hostname()
			if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
				return nil
			}
		}
	}
	return fmt.Errorf("credential-bearing base_url must use https (http is allowed only for loopback hosts)")
}
