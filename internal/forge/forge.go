// Package forge probes the official GitHub and GitLab command-line tools and
// infers which tool owns a forge URL. Ycc shells out to gh and glab rather than
// adding forge API dependencies: those CLIs already handle credentials, SSO,
// OAuth refresh, and enterprise hosts in ycc's local-daemon trust model.
//
// The tools must be installed and authenticated in the environment where the
// daemon runs. Installing or authenticating a CLI on an attaching client does
// not make it available to a remote daemon.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

// Kind identifies a supported forge implementation.
type Kind string

const (
	GitHub Kind = "github"
	GitLab Kind = "gitlab"
)

// Kinds returns the supported forges in stable probe order.
func Kinds() []Kind {
	return []Kind{GitHub, GitLab}
}

// CLI returns the official command-line tool for the forge.
func (k Kind) CLI() string {
	switch k {
	case GitHub:
		return "gh"
	case GitLab:
		return "glab"
	default:
		return ""
	}
}

// LoginCommand returns the command that authenticates the forge CLI.
func (k Kind) LoginCommand() string {
	if cli := k.CLI(); cli != "" {
		return cli + " auth login"
	}
	return ""
}

var (
	ErrNotInstalled       = errors.New("forge CLI is not installed")
	ErrNotAuthenticated   = errors.New("forge CLI is not authenticated")
	ErrUnsupportedForge   = errors.New("unsupported forge")
	versionPattern        = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)
	loggedInHostPattern   = regexp.MustCompile(`(?i)logged in to\s+([^\s]+)`)
	scpStyleRemotePattern = regexp.MustCompile(`^(?:[^@\s/:]+@)?([^:\s/]+):.+$`)
)

// Status is the result of probing one forge CLI. Detail is a short, single-line
// reason when the CLI is missing or unauthenticated.
type Status struct {
	Kind          Kind
	CLI           string
	Installed     bool
	Version       string
	Authenticated bool
	Hosts         []string
	Detail        string
}

// RunFunc runs a CLI command and returns its combined output.
type RunFunc func(ctx context.Context, name string, args ...string) (output string, err error)

// Prober permits command execution and PATH lookup to be replaced in tests. A
// zero-value Prober uses exec.CommandContext and exec.LookPath.
type Prober struct {
	Run  RunFunc
	Look func(string) (string, error)
}

// Probe checks whether the forge CLI is installed and authenticated. It does
// not contact a forge itself; any work done by auth status belongs to the CLI.
func (p Prober) Probe(ctx context.Context, k Kind) Status {
	cli := k.CLI()
	status := Status{Kind: k, CLI: cli}
	if cli == "" {
		status.Detail = fmt.Sprintf("unknown forge kind %q", k)
		return status
	}

	look := p.Look
	if look == nil {
		look = exec.LookPath
	}
	if _, err := look(cli); err != nil {
		status.Detail = cli + " not found on PATH"
		return status
	}
	status.Installed = true

	run := p.Run
	if run == nil {
		run = runCommand
	}
	if output, _ := run(ctx, cli, "--version"); output != "" {
		status.Version = versionPattern.FindString(output)
	}

	authOutput, authErr := run(ctx, cli, "auth", "status")
	status.Authenticated = authErr == nil
	if authErr == nil {
		status.Hosts = parseLoggedInHosts(authOutput)
	} else {
		status.Detail = firstLine(authOutput)
		if status.Detail == "" {
			status.Detail = firstLine(authErr.Error())
		}
		if status.Detail == "" {
			status.Detail = cli + " auth status failed"
		}
	}
	return status
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}

// Probe checks a forge with the default process runner and PATH lookup.
func Probe(ctx context.Context, k Kind) Status {
	return (Prober{}).Probe(ctx, k)
}

// readinessError keeps an actionable top-level message while supporting
// errors.Is checks against the stable sentinel cause.
type readinessError struct {
	message string
	cause   error
}

func (e *readinessError) Error() string { return e.message }
func (e *readinessError) Unwrap() error { return e.cause }

// Ready reports whether the probed CLI can be used, with an actionable error
// naming the missing CLI or login command.
func (s Status) Ready() error {
	cli := s.CLI
	if cli == "" {
		cli = s.Kind.CLI()
	}
	if cli == "" {
		return &readinessError{
			message: fmt.Sprintf("forge %q is not a supported forge (github/gitlab)", s.Kind),
			cause:   ErrUnsupportedForge,
		}
	}
	if !s.Installed {
		forgeName, installURL := "GitHub", "https://cli.github.com"
		if s.Kind == GitLab {
			forgeName, installURL = "GitLab", "https://gitlab.com/gitlab-org/cli"
		}
		return &readinessError{
			message: fmt.Sprintf("%s is not installed (needed for %s issue import / PR publish); install it from %s", cli, forgeName, installURL),
			cause:   ErrNotInstalled,
		}
	}
	if !s.Authenticated {
		login := s.Kind.LoginCommand()
		if login == "" {
			login = cli + " auth login"
		}
		return &readinessError{
			message: fmt.Sprintf("%s is installed but not authenticated; run `%s`", cli, login),
			cause:   ErrNotAuthenticated,
		}
	}
	return nil
}

// Require probes a forge with the default prober and returns an actionable error
// unless its CLI is installed and authenticated.
func Require(ctx context.Context, k Kind) error {
	return Probe(ctx, k).Ready()
}

func parseLoggedInHosts(output string) []string {
	matches := loggedInHostPattern.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	hosts := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		host := strings.ToLower(strings.Trim(match[1], " \t\r\n.,:;()[]{}"))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

func firstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Detect infers the forge kind and normalized host from an issue URL or git
// remote. It accepts ordinary URLs, ssh:// remotes, and scp-style SSH remotes.
func Detect(rawURL string) (Kind, string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "", fmt.Errorf("%w: empty URL or git remote", ErrUnsupportedForge)
	}

	host, err := extractHost(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("%w: could not parse %q: %v", ErrUnsupportedForge, rawURL, err)
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "", "", fmt.Errorf("%w: %q has no host", ErrUnsupportedForge, rawURL)
	}

	switch {
	case host == "github.com", strings.HasPrefix(host, "github."), strings.Contains(host, "github"):
		return GitHub, host, nil
	case host == "gitlab.com", strings.HasPrefix(host, "gitlab."), strings.Contains(host, "gitlab"):
		return GitLab, host, nil
	default:
		return "", host, fmt.Errorf("%w: host %q is not a supported forge (github/gitlab)", ErrUnsupportedForge, host)
	}
}

func extractHost(raw string) (string, error) {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", err
		}
		if parsed.Hostname() == "" {
			return "", errors.New("URL has no host")
		}
		return parsed.Hostname(), nil
	}

	if match := scpStyleRemotePattern.FindStringSubmatch(raw); len(match) == 2 {
		return match[1], nil
	}

	// Be liberal with copied remotes that omit a scheme, such as
	// github.com/owner/repo.git, while still requiring a host-like first segment.
	parsed, err := url.Parse("//" + raw)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" || !strings.Contains(host, ".") {
		return "", errors.New("input is not a URL or git remote")
	}
	return host, nil
}
