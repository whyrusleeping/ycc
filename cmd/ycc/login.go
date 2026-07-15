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
	"github.com/whyrusleeping/ycc/internal/openaiauth"
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
// in its [models.X] block. Logout = `ycc token rm <KEY>`. Purely local; no
// daemon needed.
func loginCommand() *cli.Command {
	return &cli.Command{
		Name:      "login",
		Usage:     "authenticate a provider subscription via OAuth (anthropic | openai)",
		ArgsUsage: "anthropic|openai",
		Description: "Logs into a Claude (Pro/Max) or ChatGPT (Plus/Pro) subscription so models can be\n" +
			"used without an API key: set `auth = \"oauth\"` on the model in ycc.toml.\n" +
			"Credentials are stored in the machine-local secrets store (never in ycc.toml);\n" +
			"remove them with `ycc token rm ANTHROPIC_OAUTH` / `ycc token rm OPENAI_OAUTH`.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			provider := cmd.Args().First()
			if provider == "" {
				provider = "anthropic"
			}
			switch provider {
			case "anthropic":
				return loginAnthropic(ctx)
			case "openai":
				return loginOpenAI(ctx)
			default:
				return fmt.Errorf("unsupported provider %q (want \"anthropic\" or \"openai\")", provider)
			}
		},
	}
}

func loginAnthropic(ctx context.Context) error {
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
	fmt.Println("Enable it on a model in ycc.toml with:  auth = \"oauth\"   (anthropic backend)")
	return nil
}

func loginOpenAI(ctx context.Context) error {
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
	fmt.Println("Enable it on a model in ycc.toml with:  auth = \"oauth\"   (openai backend)")
	fmt.Printf("Codex models: %s\n", strings.Join(codex.Models, ", "))
	return nil
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
