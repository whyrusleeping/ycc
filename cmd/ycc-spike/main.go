// Command ycc-spike is the M0 proof: a single agent, backed by any gollama
// backend, that completes a coding task in a workspace directory using the
// worker tools. It is throwaway scaffolding — the real entrypoints are yccd and
// ycc (M1+) — but it exercises the engine, tools, and event packages end to end.
//
// Usage:
//
//	export ANTHROPIC_API_KEY=...
//	ycc-spike -dir ./scratch "add a hello.txt file that says hi"
//
// Flags let you point at other backends:
//
//	ycc-spike -base-url http://localhost:11434/v1 -model qwen2.5-coder -dir ./scratch "..."
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/whyrusleeping/gollama"
	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/event"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const systemPrompt = `You are an autonomous coding agent working inside a single workspace directory.

You complete the user's task by using the provided tools to read, search, and modify
files and to run shell commands. Work concretely: inspect the workspace before changing
it, make the smallest change that satisfies the task, and verify your work (build/run/test)
when feasible.

When — and only when — the task is fully complete, call the finish tool with a concise
report of what you did. Do not call finish until the work is actually done.`

func main() {
	dir := flag.String("dir", ".", "workspace directory the agent operates in")
	model := flag.String("model", "claude-opus-4-8", "model id")
	baseURL := flag.String("base-url", "https://api.anthropic.com", "API base URL")
	keyEnv := flag.String("key-env", "ANTHROPIC_API_KEY", "env var holding the API key")
	bearer := flag.Bool("bearer", false, "send the key as a Bearer token (OpenAI-compatible) instead of x-api-key")
	maxTok := flag.Int("max-tokens", config.DefaultMaxTokens, "max tokens per turn")
	maxTurns := flag.Int("max-turns", 40, "maximum agent turns")
	flag.Parse()

	task := flag.Arg(0)
	if task == "" {
		fmt.Fprintln(os.Stderr, "usage: ycc-spike [flags] \"<task>\"")
		flag.PrintDefaults()
		os.Exit(2)
	}

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		fatal(err)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		fatal(err)
	}

	client := gollama.NewClient(*baseURL)
	if key := os.Getenv(*keyEnv); key != "" {
		if *bearer {
			client.SetBearerToken(key)
		} else {
			// Non-bearer => native Anthropic transport. Pin it explicitly so
			// prompt caching (cache_control breakpoints) works even when the
			// base URL doesn't match gollama's "anthropic.com" auto-detection.
			client.SetAnthropicMode(true)
			client.SetAPIKey(key)
		}
	} else if *keyEnv != "" {
		fmt.Fprintf(os.Stderr, "warning: %s is not set\n", *keyEnv)
	}

	emitter := event.NewEmitter(event.NewStdoutRecorder(os.Stdout), "agent")
	emitter.Emit(event.SessionStarted, map[string]any{
		"msg": fmt.Sprintf("workspace=%s model=%s backend=%s", absDir, *model, client.Backend()),
	})

	reg := tools.New()
	reg.Add(tools.Worker(&tools.Workspace{Root: absDir})...)

	loop := &engine.Loop{
		Client:    client,
		Model:     *model,
		ModelName: *model,
		Backend:   client.Backend().String(),
		System:    systemPrompt,
		Tools:     reg,
		Emitter:   emitter,
		MaxTurns:  *maxTurns,
		MaxTok:    *maxTok,
	}
	loop.Seed(task)

	res, err := loop.Run(context.Background())
	if err != nil {
		// A model-turn failure was already recorded as a session_error by the
		// engine loop (engine.TurnError marks that); only record other errors.
		var te *engine.TurnError
		if !errors.As(err, &te) {
			emitter.Emit(event.SessionError, map[string]any{"msg": err.Error()})
		}
		fatal(err)
	}
	emitter.Emit(event.SessionIdle, map[string]any{"msg": fmt.Sprintf("done in %d turns", res.Turns)})
	fmt.Printf("\n=== REPORT ===\n%s\n", res.Report)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
