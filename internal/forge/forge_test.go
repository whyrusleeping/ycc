package forge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestProber(t *testing.T) {
	tests := []struct {
		name    string
		kind    Kind
		lookErr error
		outputs map[string]struct {
			output string
			err    error
		}
		want Status
	}{
		{
			name:    "not installed",
			kind:    GitHub,
			lookErr: errors.New("not found"),
			want: Status{
				Kind: GitHub, CLI: "gh", Detail: "gh not found on PATH",
			},
		},
		{
			name: "github authenticated with multiple hosts",
			kind: GitHub,
			outputs: map[string]struct {
				output string
				err    error
			}{
				"gh --version":   {output: "gh version 2.62.0 (2024-10-30)\nhttps://github.com/cli/cli/releases/tag/v2.62.0\n"},
				"gh auth status": {output: "github.com\n  ✓ Logged in to github.com account octocat (keyring)\n\ngithub.mycorp.com\n  ✓ Logged in to github.mycorp.com account mona (keyring)\n  - Active account: true\n  ✓ Logged in to github.com account duplicate\n"},
			},
			want: Status{
				Kind: GitHub, CLI: "gh", Installed: true, Version: "2.62.0", Authenticated: true,
				Hosts: []string{"github.com", "github.mycorp.com"},
			},
		},
		{
			name: "gitlab authenticated",
			kind: GitLab,
			outputs: map[string]struct {
				output string
				err    error
			}{
				"glab --version":   {output: "glab 1.53.0 (7e582ec)\n"},
				"glab auth status": {output: "gitlab.com\n  ✓ Logged in to gitlab.com as tanuki (/home/user/.config/glab-cli/config.yml)\n  ✓ Git operations for gitlab.com configured to use ssh protocol.\n"},
			},
			want: Status{
				Kind: GitLab, CLI: "glab", Installed: true, Version: "1.53.0", Authenticated: true,
				Hosts: []string{"gitlab.com"},
			},
		},
		{
			name: "installed but unauthenticated",
			kind: GitHub,
			outputs: map[string]struct {
				output string
				err    error
			}{
				"gh --version":   {output: "gh version 2.45.1\n"},
				"gh auth status": {output: "You are not logged into any GitHub hosts.\nRun gh auth login to authenticate.\n", err: errors.New("exit status 1")},
			},
			want: Status{
				Kind: GitHub, CLI: "gh", Installed: true, Version: "2.45.1",
				Detail: "You are not logged into any GitHub hosts.",
			},
		},
		{
			name: "unparseable version remains installed",
			kind: GitLab,
			outputs: map[string]struct {
				output string
				err    error
			}{
				"glab --version":   {output: "development build\n"},
				"glab auth status": {output: "Logged in to gitlab.example.com\n"},
			},
			want: Status{
				Kind: GitLab, CLI: "glab", Installed: true, Authenticated: true,
				Hosts: []string{"gitlab.example.com"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			p := Prober{
				Look: func(name string) (string, error) {
					calls = append(calls, "look "+name)
					if tt.lookErr != nil {
						return "", tt.lookErr
					}
					return "/test/bin/" + name, nil
				},
				Run: func(_ context.Context, name string, args ...string) (string, error) {
					key := strings.Join(append([]string{name}, args...), " ")
					calls = append(calls, key)
					result, ok := tt.outputs[key]
					if !ok {
						t.Fatalf("unexpected command %q", key)
					}
					return result.output, result.err
				},
			}

			got := p.Probe(context.Background(), tt.kind)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Probe() = %#v, want %#v", got, tt.want)
			}
			if tt.lookErr != nil && len(calls) != 1 {
				t.Fatalf("missing CLI should run no commands; calls = %v", calls)
			}
		})
	}
}

func TestStatusReady(t *testing.T) {
	tests := []struct {
		name        string
		status      Status
		wantErr     error
		wantStrings []string
	}{
		{
			name:        "github missing",
			status:      Status{Kind: GitHub, CLI: "gh"},
			wantErr:     ErrNotInstalled,
			wantStrings: []string{"gh is not installed", "GitHub issue import / PR publish", "https://cli.github.com"},
		},
		{
			name:        "gitlab missing",
			status:      Status{Kind: GitLab, CLI: "glab"},
			wantErr:     ErrNotInstalled,
			wantStrings: []string{"glab is not installed", "GitLab issue import / PR publish", "https://gitlab.com/gitlab-org/cli"},
		},
		{
			name:        "github unauthenticated",
			status:      Status{Kind: GitHub, CLI: "gh", Installed: true},
			wantErr:     ErrNotAuthenticated,
			wantStrings: []string{"gh is installed but not authenticated", "`gh auth login`"},
		},
		{
			name:        "gitlab unauthenticated",
			status:      Status{Kind: GitLab, CLI: "glab", Installed: true},
			wantErr:     ErrNotAuthenticated,
			wantStrings: []string{"glab is installed but not authenticated", "`glab auth login`"},
		},
		{
			name:   "ready",
			status: Status{Kind: GitHub, CLI: "gh", Installed: true, Authenticated: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.status.Ready()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Ready() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Ready() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
			for _, want := range tt.wantStrings {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Ready() error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestRequireMissingCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := Require(context.Background(), GitHub)
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Require() error = %v, want ErrNotInstalled", err)
	}
	if !strings.Contains(err.Error(), "gh") {
		t.Fatalf("Require() error should name gh: %v", err)
	}
}

func TestDetect(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantKind Kind
		wantHost string
		wantErr  bool
	}{
		{name: "github issue", raw: "https://github.com/owner/repo/issues/42", wantKind: GitHub, wantHost: "github.com"},
		{name: "github https remote", raw: "https://User@example@github.com:443/owner/repo.git", wantKind: GitHub, wantHost: "github.com"},
		{name: "github scp remote", raw: "git@github.com:owner/repo.git", wantKind: GitHub, wantHost: "github.com"},
		{name: "gitlab ssh remote", raw: "ssh://git@gitlab.example.com:2222/owner/repo.git", wantKind: GitLab, wantHost: "gitlab.example.com"},
		{name: "gitlab issue", raw: "https://www.GitLab.com/owner/repo/-/issues/7", wantKind: GitLab, wantHost: "gitlab.com"},
		{name: "github enterprise", raw: "https://github.mycorp.com/owner/repo", wantKind: GitHub, wantHost: "github.mycorp.com"},
		{name: "scheme omitted remote", raw: "gitlab.internal.example/owner/repo.git", wantKind: GitLab, wantHost: "gitlab.internal.example"},
		{name: "unsupported", raw: "https://bitbucket.org/owner/repo/issues/1", wantHost: "bitbucket.org", wantErr: true},
		{name: "garbage", raw: "definitely not a URL", wantErr: true},
		{name: "empty", raw: "  ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, host, err := Detect(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, ErrUnsupportedForge) {
					t.Fatalf("Detect(%q) error = %v, want ErrUnsupportedForge", tt.raw, err)
				}
				if host != tt.wantHost {
					t.Fatalf("Detect(%q) host = %q, want %q", tt.raw, host, tt.wantHost)
				}
				return
			}
			if err != nil || kind != tt.wantKind || host != tt.wantHost {
				t.Fatalf("Detect(%q) = (%q, %q, %v), want (%q, %q, nil)", tt.raw, kind, host, err, tt.wantKind, tt.wantHost)
			}
		})
	}
}

func TestKindsAndCommands(t *testing.T) {
	if got, want := fmt.Sprint(Kinds()), "[github gitlab]"; got != want {
		t.Fatalf("Kinds() = %s, want %s", got, want)
	}
	if GitHub.CLI() != "gh" || GitHub.LoginCommand() != "gh auth login" {
		t.Fatalf("unexpected GitHub commands: %q, %q", GitHub.CLI(), GitHub.LoginCommand())
	}
	if GitLab.CLI() != "glab" || GitLab.LoginCommand() != "glab auth login" {
		t.Fatalf("unexpected GitLab commands: %q, %q", GitLab.CLI(), GitLab.LoginCommand())
	}
}
