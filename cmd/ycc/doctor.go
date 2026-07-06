package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	cli "github.com/urfave/cli/v3"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/daemon"
	"github.com/whyrusleeping/ycc/internal/docs"
	"github.com/whyrusleeping/ycc/internal/sandbox"
	"github.com/whyrusleeping/ycc/internal/secrets"
)

// doctorCommand is the daemon-free one-shot environment/config health check. It
// probes the whole stack — config discovery, per-model key resolution, daemon
// reachability, reviewer sandbox, git, docs, and the web-tools key — and prints
// one ✓/⚠/✗ line per check, each failure/degradation with a one-line remedy. It
// exits non-zero when any HARD failure (an unresolvable model key or a malformed
// config) is found, so it is scriptable and the natural thing to run in a bug
// report. Like `ycc spec-check` it runs locally against the workspace and needs
// no daemon; the daemon check is a best-effort probe.
func (a *app) doctorCommand() *cli.Command {
	return &cli.Command{
		Name:  "doctor",
		Usage: "one-shot environment/config health check with remedies",
		Description: "Checks the whole ycc stack and prints one ✓/⚠/✗ line per check: config file\n" +
			"discovery, each configured model's key resolution (env / secrets store / MISSING),\n" +
			"persistent daemon reachability, reviewer sandbox mechanism, git repo state, the docs\n" +
			"entry point + backlog, and the EXA_API_KEY web-tools key. Every ✗/⚠ comes with a\n" +
			"one-line remedy.\n\n" +
			"Runs locally against the workspace; no daemon is required (daemon checks are\n" +
			"best-effort probes). Exits non-zero when a hard failure — an unresolvable model key\n" +
			"or a malformed config — is found, so it works in scripts and CI.",
		Action: func(ctx context.Context, cmd *cli.Command) error {
			hardFail := runDoctor(a.workspace, a.configPath, a.addr, a.token, os.Stdout)
			if hardFail {
				return cli.Exit("", 1)
			}
			return nil
		},
	}
}

// checkStatus is a single doctor line's severity.
type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

// check is one health-check result: a status, a short label, a detail string,
// and an optional one-line remedy shown (indented) on warnings/failures.
type check struct {
	status checkStatus
	label  string
	detail string
	remedy string
}

func (c checkStatus) symbol() string {
	switch c {
	case statusOK:
		return "✓"
	case statusWarn:
		return "⚠"
	default:
		return "✗"
	}
}

// runDoctor is the testable core of `ycc doctor`. It runs every check against
// the workspace/flags, renders the results to out, and reports whether any HARD
// failure (unresolvable model key or malformed config) was found — the caller
// maps that to a non-zero exit. It never prints secret values, only where a key
// resolved from. All checks are best-effort and non-mutating (in particular the
// git probe never runs `git init`).
func runDoctor(workspace, configPath, addr, token string, out io.Writer) (hardFail bool) {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		ws = workspace
	}

	var checks []check
	add := func(c check) { checks = append(checks, c) }

	// 1. Config file + 2. model keys.
	cfg, cfgChecks := configChecks(ws, configPath)
	for _, c := range cfgChecks {
		add(c)
	}
	for _, c := range modelKeyChecks(cfg) {
		add(c)
	}

	// 3. Daemon (best-effort probe, never hard).
	add(daemonCheck(addr, token))

	// 4. Sandbox.
	add(sandboxCheck())

	// 5. Git (never hard).
	add(gitCheck(ws))

	// 6. Docs (spec entry point + backlog).
	for _, c := range docsChecks(ws) {
		add(c)
	}

	// 7. EXA_API_KEY (web tools).
	add(exaCheck())

	// Render.
	var ok, warn, fail int
	for _, c := range checks {
		fmt.Fprintf(out, "%s %s: %s\n", c.status.symbol(), c.label, c.detail)
		if c.status != statusOK && c.remedy != "" {
			fmt.Fprintf(out, "  ↳ %s\n", c.remedy)
		}
		switch c.status {
		case statusOK:
			ok++
		case statusWarn:
			warn++
		default:
			fail++
		}
	}
	fmt.Fprintf(out, "\ndoctor: %d ok, %d warning(s), %d failure(s)\n", ok, warn, fail)
	return fail > 0
}

// configChecks resolves and loads the config file, returning the loaded config
// (nil when none/malformed) and the config-file check line(s). A malformed or
// invalid config is a HARD failure (statusFail).
func configChecks(ws, configPath string) (*config.Config, []check) {
	path := configPath
	if path == "" {
		path = daemon.DiscoverConfig(ws)
	}
	if path == "" {
		return nil, []check{{
			status: statusWarn,
			label:  "config file",
			detail: "no ycc.toml found; the built-in Anthropic fallback will be used",
			remedy: "run `ycc` to launch the first-run setup wizard, or write a ycc.toml",
		}}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, []check{{
			status: statusFail,
			label:  "config file",
			detail: fmt.Sprintf("%s failed to load: %v", relOrAbs(ws, path), err),
			remedy: "fix the reported error in " + relOrAbs(ws, path),
		}}
	}
	return cfg, []check{{
		status: statusOK,
		label:  "config file",
		detail: "loaded " + relOrAbs(ws, path),
	}}
}

// modelKeyChecks resolves each configured model's key with the same precedence
// as config.resolveKey (env, then secrets store). A model whose key cannot be
// resolved is a HARD failure. With no config file, the built-in Anthropic
// fallback (ANTHROPIC_API_KEY) is checked the same way — every session would
// 401 without it, so a missing fallback key is also HARD.
func modelKeyChecks(cfg *config.Config) []check {
	if cfg == nil {
		return []check{keyCheck("model key (fallback claude)", "ANTHROPIC_API_KEY")}
	}
	names := make([]string, 0, len(cfg.Models))
	for name := range cfg.Models {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []check
	for _, name := range names {
		m := cfg.Models[name]
		out = append(out, keyCheck("model key ("+name+")", m.KeyEnv))
	}
	return out
}

// keyCheck resolves a single key_env, reporting where it resolved from without
// ever printing the value. An empty keyEnv is a keyless backend (e.g. ollama);
// an unresolvable non-empty keyEnv is a HARD failure with a `ycc token set`
// remedy.
func keyCheck(label, keyEnv string) check {
	if keyEnv == "" {
		return check{status: statusOK, label: label, detail: "no key required (keyless backend)"}
	}
	if os.Getenv(keyEnv) != "" {
		return check{status: statusOK, label: label, detail: fmt.Sprintf("%s resolved from env", keyEnv)}
	}
	if _, ok := secrets.Lookup(keyEnv); ok {
		return check{status: statusOK, label: label, detail: fmt.Sprintf("%s resolved from secrets store", keyEnv)}
	}
	return check{
		status: statusFail,
		label:  label,
		detail: keyEnv + " MISSING (not in env or secrets store)",
		remedy: fmt.Sprintf("run `ycc token set %s` (or export %s)", keyEnv, keyEnv),
	}
}

// daemonCheck probes the persistent daemon; it is best-effort and never a hard
// failure. With --addr set it probes that (with --token); otherwise it probes
// the local loopback daemon.
func daemonCheck(addr, token string) check {
	target, tok := addr, token
	if target == "" {
		target = daemon.LocalAddr
	}
	if daemon.Reachable(target, tok) {
		return check{status: statusOK, label: "daemon", detail: "persistent daemon reachable at " + target}
	}
	return check{
		status: statusWarn,
		label:  "daemon",
		detail: "no persistent daemon running (plain `ycc` starts a one-shot in-process daemon)",
		remedy: "run `ycc daemon` or use `ycc --background` for persistence",
	}
}

// sandboxCheck reports the reviewer bash confinement mechanism. Landlock/bwrap
// are ✓; None degrades to prompt-only enforcement (⚠) with a platform-aware
// remedy.
func sandboxCheck() check {
	switch sandbox.Available() {
	case sandbox.Landlock:
		return check{status: statusOK, label: "sandbox", detail: "reviewer bash confined via Landlock"}
	case sandbox.Bwrap:
		return check{status: statusOK, label: "sandbox", detail: "reviewer bash confined via bubblewrap (bwrap)"}
	default:
		c := check{
			status: statusWarn,
			label:  "sandbox",
			detail: "no sandbox mechanism; reviewer bash confinement is prompt-only",
		}
		if runtime.GOOS == "linux" {
			c.remedy = "install bubblewrap (bwrap) or run a Landlock-capable kernel (>= 5.13)"
		} else {
			c.detail += " (not supported on " + runtime.GOOS + ")"
		}
		return c
	}
}

// gitCheck probes the workspace's git state WITHOUT mutating it — it never runs
// `git init`. Git issues are informational (never hard failures).
func gitCheck(ws string) check {
	if _, err := exec.LookPath("git"); err != nil {
		return check{
			status: statusWarn,
			label:  "git",
			detail: "git binary not found on PATH",
			remedy: "install git",
		}
	}
	inside, err := runGit(ws, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return check{
			status: statusWarn,
			label:  "git",
			detail: "not a git repository (ycc will run git init on the first session)",
		}
	}
	status, err := runGit(ws, "status", "--porcelain")
	if err != nil {
		return check{status: statusWarn, label: "git", detail: "git repo present (status unavailable)"}
	}
	n := 0
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n == 0 {
		return check{status: statusOK, label: "git", detail: "git repo present, working tree clean"}
	}
	return check{status: statusWarn, label: "git", detail: fmt.Sprintf("git repo present, %d uncommitted change(s)", n)}
}

// runGit runs a read-only git command in dir and returns its stdout.
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// docsChecks reports the spec entry point and the backlog directory. Both are
// informational (⚠, never hard) since ycc creates them on demand.
func docsChecks(ws string) []check {
	store := docs.NewStore(ws)
	var out []check

	sp := store.SpecPath()
	if fi, err := os.Stat(sp); err == nil && !fi.IsDir() {
		out = append(out, check{status: statusOK, label: "docs", detail: "spec entry point at " + relOrAbs(ws, sp)})
	} else {
		out = append(out, check{
			status: statusWarn,
			label:  "docs",
			detail: "no spec entry point (" + relOrAbs(ws, sp) + " not found)",
			remedy: "create spec.md (or set spec_path in .ycc/config.toml)",
		})
	}

	dir := store.Dir()
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		detail := "backlog present at " + relOrAbs(ws, dir)
		if tasks, err := store.List(); err == nil {
			detail = fmt.Sprintf("%s (%d task(s))", detail, len(tasks))
		}
		out = append(out, check{status: statusOK, label: "backlog", detail: detail})
	} else {
		out = append(out, check{
			status: statusWarn,
			label:  "backlog",
			detail: "no backlog/ yet (created when the first task is added)",
		})
	}
	return out
}

// exaCheck reports the EXA_API_KEY used by the web_search / fetch_page tools.
// Missing is a degradation (⚠), never hard: those tools simply disable.
func exaCheck() check {
	if os.Getenv("EXA_API_KEY") != "" {
		return check{status: statusOK, label: "web tools", detail: "EXA_API_KEY resolved from env"}
	}
	if _, ok := secrets.Lookup("EXA_API_KEY"); ok {
		return check{status: statusOK, label: "web tools", detail: "EXA_API_KEY resolved from secrets store"}
	}
	return check{
		status: statusWarn,
		label:  "web tools",
		detail: "EXA_API_KEY missing; web_search / fetch_page are disabled",
		remedy: "run `ycc token set EXA_API_KEY`",
	}
}

// relOrAbs returns path relative to ws when possible (for compact output),
// falling back to the absolute path.
func relOrAbs(ws, path string) string {
	if rel, err := filepath.Rel(ws, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}
