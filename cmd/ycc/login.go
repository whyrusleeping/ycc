package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"github.com/whyrusleeping/ycc/internal/anthropicauth"
	"github.com/whyrusleeping/ycc/internal/codex"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/openaiauth"
	"github.com/whyrusleeping/ycc/internal/setup"
)

// loginCommand authenticates a provider subscription via OAuth (spec §13).
// Two providers are supported:
//   - anthropic (Claude Pro/Max): paste-code flow — the login page shows a
//     code#state string the user pastes back.
//   - openai (ChatGPT Plus/Pro): browser flow with a local callback server on
//     localhost:1455 (the redirect the Codex public client id is registered
//     with), so the browser must run on this machine (or the port forwarded).
//
// Credentials land in the machine-local secrets store (never in ycc.toml)
// under ANTHROPIC_OAUTH / OPENAI_OAUTH; a model opts in with `auth = "oauth"`
// in its [models.X] block. After a successful login the config is updated
// automatically (see configureSubscriptionModel) so the subscription is usable
// on the next `ycc` run without hand-editing ycc.toml. Logout = `ycc token rm
// <KEY>`. Purely local; no daemon needed.
func (a *app) loginCommand() *cli.Command {
	return &cli.Command{
		Name:      "login",
		Usage:     "authenticate a provider subscription via OAuth (anthropic | openai)",
		ArgsUsage: "anthropic|openai",
		Description: "Logs into a Claude (Pro/Max) or ChatGPT (Plus/Pro) subscription so models can be\n" +
			"used without an API key, and configures a matching `auth = \"oauth\"` model in\n" +
			"ycc.toml (creating the config if needed) so it is ready on the next run.\n" +
			"Credentials are stored in the machine-local secrets store (never in ycc.toml);\n" +
			"remove them with `ycc token rm ANTHROPIC_OAUTH` / `ycc token rm OPENAI_OAUTH`.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			provider := cmd.Args().First()
			if provider == "" {
				provider = "anthropic"
			}
			switch provider {
			case "anthropic":
				return a.loginAnthropic(ctx)
			case "openai":
				return a.loginOpenAI(ctx)
			default:
				return fmt.Errorf("unsupported provider %q (want \"anthropic\" or \"openai\")", provider)
			}
		},
	}
}

func (a *app) loginAnthropic(ctx context.Context) error {
	pkce, err := anthropicauth.NewPKCE()
	if err != nil {
		return fmt.Errorf("generating PKCE challenge: %w", err)
	}
	authURL := anthropicauth.AuthorizeURL(pkce)
	fmt.Println("Open this URL in your browser and log in with your Claude account:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()
	openBrowser(authURL)
	fmt.Print("Paste the code shown after login (looks like code#state): ")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("reading code: %w", err)
	}
	code := strings.TrimSpace(line)
	if code == "" {
		return fmt.Errorf("empty code, aborting")
	}
	creds, err := anthropicauth.Exchange(ctx, code, pkce)
	if err != nil {
		return fmt.Errorf("exchanging code: %w", err)
	}
	if err := anthropicauth.Save(creds); err != nil {
		return fmt.Errorf("storing credentials: %w", err)
	}
	fmt.Printf("Logged in. Access token valid until %s (auto-refreshed after that).\n",
		time.Unix(creds.ExpiresAt, 0).Format(time.RFC1123))
	a.configureSubscriptionModel("anthropic")
	return nil
}

func (a *app) loginOpenAI(ctx context.Context) error {
	// The whole flow (including the human logging in) is bounded; the local
	// callback server is torn down when Login returns.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	creds, err := openaiauth.Login(ctx, func(url string) {
		fmt.Println("Open this URL in your browser and log in with your ChatGPT account:")
		fmt.Println()
		fmt.Println("  " + url)
		fmt.Println()
		fmt.Println("Waiting for the browser to complete login (callback on localhost:1455)…")
		openBrowser(url)
	})
	if err != nil {
		return err
	}
	if err := openaiauth.Save(creds); err != nil {
		return fmt.Errorf("storing credentials: %w", err)
	}
	fmt.Printf("Logged in (ChatGPT account %s). Access token valid until %s (auto-refreshed).\n",
		creds.AccountID, time.Unix(creds.ExpiresAt, 0).Format(time.RFC1123))
	fmt.Printf("Codex models: %s\n", strings.Join(codex.Models, ", "))
	a.configureSubscriptionModel("openai")
	return nil
}

// configureSubscriptionModel updates ycc.toml after a successful login so the
// subscription is usable on the next run without hand-editing config: it adds
// an `auth = "oauth"` model for the backend (or reports the one already
// configured), creating a fresh config — with roles pointed at the new model —
// when none exists yet. Best-effort: on any failure it prints the manual
// instruction instead of failing the login (the credentials are already
// stored).
func (a *app) configureSubscriptionModel(backend string) {
	manual := func() {
		fmt.Printf("Enable it on a model in ycc.toml with:  auth = \"oauth\"   (%s backend)\n", backend)
	}
	path := a.configPath
	if path == "" {
		path = daemon.DiscoverConfig(a.workspace)
	}
	var cfg *config.Config
	created := false
	switch {
	case path == "":
		// No config anywhere: create one in the user config dir (the same
		// place the first-run wizard writes).
		p, err := setup.ConfigPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ycc: cannot resolve config path: %v\n", err)
			manual()
			return
		}
		path, created = p, true
		cfg = &config.Config{MaxTokens: config.DefaultMaxTokens}
	default:
		if _, err := os.Stat(path); err != nil {
			// An explicit --config pointing at a file that does not exist yet.
			created = true
			cfg = &config.Config{MaxTokens: config.DefaultMaxTokens}
			break
		}
		var err error
		cfg, err = config.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ycc: cannot update %s: %v\n", path, err)
			manual()
			return
		}
	}
	name, added, err := config.EnsureSubscriptionModel(cfg, backend)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ycc: %v\n", err)
		manual()
		return
	}
	if !added {
		fmt.Printf("Model %q in %s already uses the subscription (auth = \"oauth\").\n", name, path)
		return
	}
	if err := config.Save(path, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ycc: cannot write %s: %v\n", path, err)
		manual()
		return
	}
	if created {
		fmt.Printf("Wrote %s with model %q (auth = \"oauth\") assigned to all roles.\n", path, name)
	} else {
		fmt.Printf("Added model %q (auth = \"oauth\") to %s.\n", name, path)
		fmt.Println("Assign it to roles from the TUI settings (esc → settings) or [roles] in ycc.toml.")
	}
}

// openBrowser makes a best-effort attempt to open url in the user's browser;
// failures are silent (the URL was already printed).
func openBrowser(url string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", url)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		c = exec.Command("xdg-open", url)
	}
	_ = c.Start()
}
