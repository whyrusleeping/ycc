# ycc CLI reference

`ycc` is a single binary that is client, TUI, and daemon in one. The command tree
is built with [urfave/cli], so everything is discoverable from the binary itself:

```sh
ycc --help              # top-level: global options + command list
ycc <command> --help    # any command's options and arguments
```

This document mirrors that built-in help as a single reference.

```
ycc [global options] [command [command options]] [arguments]
```

With **no command**, `ycc` launches the interactive TUI (home menu).

## Global options

These precede the subcommand (e.g. `ycc --addr URL list`).

| Flag | Description |
|------|-------------|
| `--addr URL` | remote/explicit daemon URL to attach to |
| `--token T` | bearer token (for `--addr`); also from `$YCC_TOKEN` |
| `--workspace DIR` | workspace for new sessions (default: current directory) |
| `--config FILE` | TOML model config for the local daemon |
| `--background` | spawn a detached persistent daemon and attach (opt-in persistence) |
| `--help, -h` | show help |

See the README for the persistence model behind `--addr` / `--background` / the
default in-process daemon.

## Commands

### `ycc` (no subcommand) — TUI

Launches the interactive home menu. On a persistent/remote (multi-project) daemon
it shows the project picker first; a one-shot in-process daemon has a single
implicit project (the current directory). If there is no usable model config, a
first-run setup wizard runs and writes `~/.config/ycc/ycc.toml`.

### `ycc start [task]` — start a session and stream it

Starts a session and streams its event log to stdout. Lines you type on stdin are
sent to the agent as input (use this to prod/steer it). The `task` is an optional
leading positional — omit it in `work` mode to let the coordinator pick the next
task from the backlog.

| Flag | Description |
|------|-------------|
| `--workspace DIR` | workspace dir (default: `--workspace` or current directory) |
| `--project NAME` | registered project name (overrides `--workspace`) |
| `--mode MODE` | session mode: `chat`, `work`, or `pm` (default: `work`) |
| `--level LEVEL` | interaction level: `interactive`, `judgement`, or `autonomous` |

```sh
ycc start "add a hello.txt"
ycc start --mode pm --level interactive
ycc start                       # work mode: coordinator picks a backlog task
```

### `ycc attach <session-id>` — re-attach to a session

Re-attaches to a running (or finished) session and streams its log; stdin lines
are forwarded as input, like `start`.

| Flag | Description |
|------|-------------|
| `--from N` | replay events with seq greater than `N` (default: `0`, i.e. the whole log) |

```sh
ycc attach s_abc123            # replay everything, then live-stream
ycc attach s_abc123 --from 42  # only events after seq 42
```

### `ycc list` — list sessions

Prints one line per session: `id  mode  status  workspace`.

### `ycc modes` — list session modes

Lists the selectable session modes:

| Mode | Description |
|------|-------------|
| `chat` | Open-ended conversation and coding — no fixed workflow. |
| `work` | Pick a backlog task, implement it, review across models, and commit. |
| `pm` | Plan, document, and groom the backlog (spec.md, tasks, plans). No implementation. |

### `ycc stop <session-id>` — stop a session

Stops a running session.

### `ycc project <add|list|remove>` — manage the project registry

Manage the daemon's project registry (a persistent/remote daemon serves many
projects). `ycc project` with no subcommand lists. Alias: `ycc projects`.

| Subcommand | Description |
|------------|-------------|
| `add <path> [--name NAME]` | register a project directory (default name: directory basename) |
| `list` | list registered projects |
| `remove <name>` (alias `rm`) | remove a registered project |

```sh
ycc project add ~/code/myapp --name myapp
ycc project list
ycc project remove myapp
```

### `ycc cost` — usage & cost breakdown

Renders the usage/cost table from the daemon. By default it groups by backlog
task.

| Flag | Description |
|------|-------------|
| `--project NAME` | registered project name (default: daemon default workspace) |
| `--by LIST` | group by, comma-separated: `task`, `model`, `session`, `agent`, `day` (default: `task`) |
| `--since YYYY-MM-DD` | include usage on/after this day |
| `--until YYYY-MM-DD` | include usage on/before this day |

```sh
ycc cost
ycc cost --by model,day --since 2026-06-01 --until 2026-06-30
```

Columns: the group-by dimension(s), then `Input`, `Output`, `Cache`, `Total`
(tokens) and `Cost`. A `*` marks partial pricing (some models unpriced); `—`
marks fully unpriced rows.

### `ycc spec-check` — deterministic spec/code drift check

Deterministically checks the project's design docs for **stale references**: it
resolves the docs set (the spec entry point plus any configured `doc_globs`),
extracts the file paths, package directories, and code symbols the docs mention in
inline code spans, and reports any that no longer exist in the repository. Ambiguous
spans are skipped, so it holds a **zero-false-positive** discipline.

This is the deterministic pre-pass of the spec-doctor flow (spec §6.4), exposed as a
plain subcommand: it runs **locally against the workspace and needs no daemon**. It
uses the global `--workspace DIR` flag (default: current directory).

```sh
ycc spec-check                 # check the current workspace
ycc --workspace ../proj spec-check
```

**Exit codes** make it usable as a pre-commit / CI gate:

- `0` — every reference resolves, **or** there are no design docs to check.
- `1` — one or more stale references were found (the markdown report names them).

The markdown stale-reference report is printed to stdout. The spec-doctor `pm`
preset runs this command via `Bash` as phase 1 (falling back to
`go run ./cmd/ycc spec-check` in a dev workspace where the binary isn't on `PATH`).

### `ycc doctor` — environment/config health check

One-shot health check for the whole ycc stack. It prints one `✓`/`⚠`/`✗` line per
check, and every `✗`/`⚠` is followed by an indented `↳` one-line remedy. Like
`spec-check` it runs **locally against the workspace and needs no daemon** (the
daemon check is a best-effort probe). It's the natural thing to run in a bug report.

Checks, in order:

- **config file** — discovered `ycc.toml` (explicit `--config`, else workspace then
  user config dir); a malformed/invalid config is a hard failure.
- **model keys** — each configured model's `key_env` resolved from the environment
  or the machine-local secrets store (or `MISSING`). With no config file the built-in
  Anthropic fallback (`ANTHROPIC_API_KEY`) is checked. Secret values are never printed.
- **daemon** — whether a persistent daemon is reachable (`--addr`/`--token` or the
  local loopback); degrades to a warning otherwise.
- **sandbox** — reviewer bash confinement mechanism (Landlock / bwrap / none).
- **git** — repo present and working tree clean/dirty, or not a repo. Read-only: it
  never runs `git init`.
- **docs** — spec entry point and backlog directory (with task count).
- **web tools** — `EXA_API_KEY` presence (`web_search` / `fetch_page` disable without it).

```sh
ycc doctor                     # check the current workspace
ycc --workspace ../proj doctor
```

**Exit codes** make it scriptable:

- `0` — no hard failures (warnings are allowed).
- `1` — a **hard failure**: an unresolvable model key or a malformed config.

### `ycc token <set|list|rm>` — machine-local secrets store

Manages the machine-local secrets store. This is a purely local operation that
does **not** talk to the daemon. Keys are resolved from the environment first,
then this store, so saving a key here avoids exporting it every session. The token
value is read from **stdin** (never from argv), so it never lands in shell history.

| Subcommand | Description |
|------------|-------------|
| `set <KEY_ENV>` | store a token (prompts, or reads a piped value) |
| `list` | list stored token key names |
| `rm <KEY_ENV>` (alias `remove`) | remove a stored token |

```sh
ycc token set ANTHROPIC_API_KEY        # paste at the prompt
printf '%s' "$KEY" | ycc token set EXA_API_KEY
ycc token list
ycc token rm EXA_API_KEY
```

### `ycc daemon` — run the persistent daemon

Runs the explicit, persistent, foreground service. It serves until killed and
does not dial a client of its own.

| Flag | Default | Description |
|------|---------|-------------|
| `--addr ADDR` | `127.0.0.1:8787` | address to listen on |
| `--workspace DIR` | `.` | default workspace for sessions that don't specify one |
| `--config FILE` | | TOML config file (models + roles) |
| `--model ID` | `claude-opus-4-8` | fallback model id (when no `--config`) |
| `--base-url URL` | `https://api.anthropic.com` | fallback API base URL (when no `--config`) |
| `--key-env VAR` | `ANTHROPIC_API_KEY` | fallback API key env var (when no `--config`) |
| `--max-tokens N` | `32000` | fallback max tokens per turn (when no `--config`) |
| `--token T` | `$YCC_TOKEN` | bearer token clients must present (empty disables auth) |
| `--tls-cert FILE` | | TLS certificate file (enables HTTPS) |
| `--tls-key FILE` | | TLS key file |

```sh
ycc daemon
ycc daemon --addr 0.0.0.0:8787 --token "$YCC_TOKEN" --tls-cert c.pem --tls-key k.pem
```

## Interaction levels

The `--level` flag on `start` (and the TUI settings overlay) sets one policy value
enforced at the `ask_user` gate:

- **`interactive`** — ask freely; confirm the plan, surface meaningful choices.
- **`judgement`** — proceed on best judgement; only ask when genuinely blocked or a
  decision is hard to reverse.
- **`autonomous`** — never ask; make every call and accumulate questions /
  assumptions / decisions into the final report.

[urfave/cli]: https://github.com/urfave/cli
