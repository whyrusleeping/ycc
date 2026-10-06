# ycc

**ycc is a docs-driven coding harness.** You keep a design spec and a backlog in your
repository; ycc's agents turn backlog tasks into reviewed, committed code and keep the
documents up to date as they go. A long-running daemon does the work and you watch and steer it
from a browser, your phone, or a terminal. You can close the laptop and the work keeps going.

```
            you ──── plan & accept ───▶ backlog/  spec.md  docs/
                                            │
                                            ▼
      ycc daemon ── work session: implement → review → update task → git commit
            │
            └── event log ──▶  web UI · iOS app · CLI · TUI   (watch, answer, steer)
```

- [How ycc works](#how-ycc-works)
- [Getting started](#getting-started)
- [Day-to-day use](#day-to-day-use)
- [Clients](#clients)
- [Always-on and remote use](#always-on-and-remote-use)
- [Configuration](#configuration)
- [Credentials and secrets](#credentials-and-secrets)
- [Development](#development)

## How ycc works

### The repository is the memory

Agent sessions are disposable, but project state is not. Everything an agent needs to
continue the work lives in plain, committed files that both you and the agents edit:

| File | What it holds |
|------|---------------|
| `spec.md` (+ any `docs/` design set) | How the system is meant to work: behavior, architecture, interfaces, invariants. |
| `backlog/NNNN-*.md` | One Markdown file per task: intent, acceptance criteria, status, dependencies, and a work log. |
| `plans/` | Repeatable procedures that automation can't replace, such as release smoke tests. |
| `memory.md` | Short, advisory notes about *working on* the project (gotchas, environment quirks). |
| git history | The record of code changes. ycc works with your git history and doesn't replace it. |

Each work session starts with fresh context and rebuilds what it needs from these files. As a
result you can review, edit, or revert the project's state with an editor and `git`, and
any client or model can pick up where the last one stopped.

### The spec and the code change together

The spec is maintained alongside the code, and **a contradiction between them is a bug**. Work
sessions update the design docs in the same commit as the code they describe. `ycc spec-check`
deterministically flags paths, packages, and symbols that the docs mention but that no longer
exist. The `spec-doctor` preset goes further and compares each spec section against the code.

### You decide what gets done; agents decide how

Backlog statuses separate ideas from accepted work:

- **`proposed`**: an agent (or you) captured an idea, but nobody has agreed to do it yet.
  Proposed tasks never run.
- **`todo`**: accepted work. Promoting a task from `proposed` to `todo` is how you approve it.
- **`in_progress` → `done`**: a work session is carrying it out, and then it lands as a single
  commit.
- **`blocked`**: the task needs something only a human can provide, and the reason is recorded
  in the task.

Agents add the adjacent work they discover as *proposed* follow-ups. They don't quietly expand
the task they were given.

### Effort proportional to risk

Tests, plans, documentation, and review all have a maintenance cost, so they are used where
they help and not added by default. A tiny change gets a self-review. Ordinary work gets one
focused reviewer. Large, risky, or hard-to-reverse changes are reviewed by several models in
parallel. Reviews judge the change against the task and real failure modes, not personal taste.
[`CONTRIBUTING.md`](CONTRIBUTING.md) is the written version of this policy. ycc follows it for
its own development, and its agents apply it to your project.

### The daemon owns the work; clients are just views

The daemon runs the models, edits files, makes commits, and writes an append-only event log for
every session. The web UI, the iOS app, the CLI, and the TUI all use the same API to subscribe
to that log and send commands. A client can disconnect, crash, or go to sleep without stopping
a session. When it reconnects, it replays the log from where it stopped.

### Humans are optional but always welcome

In an *attended* session, the agent asks a question when it needs your input, and you can
answer it from any client. *Unattended* sessions (such as the work loop) never wait for an
answer: they choose a reversible assumption or mark the task blocked and move on. Operations
with a large impact always require explicit approval, even in unattended runs. Examples are
handing a plan off to implementation and merging a parallel workstream (unless you configured
automatic merging).

### Many models, each in its own role

A config file maps logical model names to providers (Anthropic, OpenAI, any OpenAI-compatible
endpoint, or Ollama). Roles assign those models to the **coordinator**, the **implementer**,
and the **reviewers**. You can use an API key or an existing Claude or ChatGPT subscription,
and mix them freely. For example, one model can plan, another can write the code, and a third
can review it.

The full design is in [`spec.md`](spec.md). The reasoning behind individual decisions is in
[`docs/design/`](docs/design/).

## Getting started

### 1. Install

```sh
go install github.com/whyrusleeping/ycc/cmd/ycc@latest   # or: go build -o ycc ./cmd/ycc
```

`ycc` is a single binary that contains the daemon, the CLI, the TUI, and the embedded web
client. Building it needs only Go. You don't need Node.

### 2. Connect a model

To use a subscription, run one of these commands. Each one opens a browser login and adds a
matching model to your config:

```sh
ycc login anthropic   # Claude Pro/Max
ycc login openai      # ChatGPT Plus/Pro
```

To use an API key instead, store the key in ycc's local secrets store and refer to it by name
from [`ycc.toml`](#configuration):

```sh
ycc token set ANTHROPIC_API_KEY
```

`ycc doctor` checks the setup: config discovery, key resolution, git state, the docs entry
point, and more. Each problem it finds comes with a fix.

### 3. Run the daemon and open the web UI

```sh
cd ~/code/myproject
ycc daemon --web
```

Open <http://127.0.0.1:8787>. The page asks for the daemon token. On loopback, the daemon
creates one for you at `~/.local/state/ycc/daemon-token`, or you can set `YCC_TOKEN` to choose
your own. The directory you start the daemon in becomes the first project, and you can add
more from the UI or with `ycc project add <path>`.

### 4. Onboard the project

Start a **pm** session using the **Onboard** preset. In an empty repository, it works with you
on the project's purpose, writes an initial `spec.md`, and creates a starter backlog. In an
existing codebase, it asks what you want to change next and documents only that part. It
doesn't inventory the whole repository before you can get anything done.

## Day-to-day use

ycc has three session **modes**:

| Mode | Use it for |
|------|------------|
| **pm** | Thinking and planning. It discusses design, edits the spec, investigates the code, and creates and grooms tasks, but it doesn't write code. Presets: *Onboard*, *Spec doctor*, *Groom project memory*. |
| **work** | Doing one accepted task from start to finish: read the task and spec, implement it (directly or through an implementer subagent), review in proportion to risk, update the task, and commit. |
| **chat** | Free-form help with direct file and shell tools and no fixed workflow. Use it for quick questions, exploration, and one-off changes. |

A typical loop looks like this:

1. **Capture** ideas whenever they come up. Use quick-capture in the web or iOS app, ask an
   agent, or run `ycc task add "title" -d "details"`.
2. **Plan** in a pm session. It sharpens the task's acceptance criteria and updates the spec,
   and you promote the task to `todo` when you agree with it.
3. **Work** a task. Start a work session on it, or let the coordinator choose the next ready
   task. The session ends with one commit that contains the code, the spec changes, and the
   updated task.
4. **Run the work loop** to keep going without supervision. The daemon starts a fresh work
   session for each ready task until none remain. It respects dependencies and budgets,
   survives client disconnects, and sends a digest when it finishes.
5. **Parallelize** with **workstreams**. Each workstream is a separate git worktree and branch
   with its own work session. When it finishes, ycc rebases it, runs your `verify` command, and
   fast-forwards the base branch. If no verify command is configured, or if you choose the gate
   mode, it waits for your approval of the merged diff instead. If the rebase conflicts or
   verification fails, ycc can send an agent to fix it first. The base branch is never left in
   a conflicted state.

While a session runs you can send input at any time. It is delivered at the next safe point.
You can also **interrupt** a session, which pauses it at a checkpoint so you can steer it and
then resume. **Stop** ends a session immediately. A finished session can be reopened later
with its full history.

Usage and cost are calculated from the event logs. You can break them down by project, task,
model, or actor in the Usage view, or from the command line with
`ycc cost --by task --since 2026-06-01`. Optional token and dollar budgets (see
[Configuration](#configuration)) cap sessions and work-loop runs.

## Clients

All clients use the same daemon API, so you can start something on one and pick it up on
another.

- **Web** (`ycc daemon --web`). A desktop browser client for the full workflow: sessions with
  live transcripts and diffs, the backlog, the work loop, workstreams, files, memory and plans,
  usage, and model, role, and review-tier settings. It includes a command palette (shortcut
  help is under `?`), image paste and drag-and-drop, and browser notifications. The source is
  in [`clients/web`](clients/web).
- **iOS** ([`clients/ios`](clients/ios)). A native iPhone app for checking in, answering
  questions, steering, capturing tasks, and starting work while you're away from your desk.
  Combined with [push notifications](#notifications), a tap on a notification opens the session
  that needs you. To build it, run `cd clients/ios && xcodegen generate`, open
  `Ycc.xcodeproj` in Xcode, choose your signing team, and run the app on your device. Then
  enter the daemon URL and token.
- **CLI**. Scriptable commands for streaming and controlling sessions and backlogs:

  ```sh
  ycc start --mode chat "why is TestFoo flaky?"   # start a session and stream it; type to steer
  ycc attach s_abc123 --from 0                    # replay a session, then follow it live
  ycc list                                        # sessions
  ycc task list                                   # backlog with readiness
  ycc export s_abc123 > session.md                # shareable Markdown transcript
  ycc spec-check                                  # CI gate for stale design references
  ```

  Every command documents itself with `ycc <command> --help`. The full reference is in
  [`docs/cli.md`](docs/cli.md). Shell completion is available through
  `source <(ycc completion bash)` (zsh and fish are also supported).
- **TUI** ([screenshot](docs/tui.png)). Running `ycc` with no arguments opens a terminal home
  menu. If a persistent daemon is running, the TUI attaches to it. Otherwise it starts a
  temporary in-process daemon that shuts down when you exit. It's handy for quick local
  sessions, but use `ycc daemon` for anything you want to keep running.

## Always-on and remote use

ycc is designed to run on a machine that stays on, such as a dev box or a home server, and to
be reached from anywhere.

| Command | Daemon lifetime |
|---------|-----------------|
| `ycc daemon [--web]` | A persistent daemon for multiple projects, running in the foreground. This is the usual setup. |
| `ycc --background` | Starts a detached persistent daemon and attaches the TUI to it. |
| `ycc` / `ycc start …` | Attaches to a local persistent daemon if one is running. Otherwise it uses a temporary in-process daemon that stops when the command exits. |
| `ycc --addr URL …` | Attaches any CLI or TUI command to a remote daemon. |

To serve the daemon beyond loopback, give it a token. The daemon refuses to bind a
non-loopback address without one:

```sh
export YCC_TOKEN=$(openssl rand -hex 32)
ycc daemon --web --addr 100.64.0.1:8787                 # e.g. a Tailscale address
YCC_TOKEN=… ycc --addr http://100.64.0.1:8787 list      # from another machine
```

The recommended setup is a private network such as Tailscale or WireGuard, which encrypts the
traffic. Without that, use `--tls-cert`/`--tls-key` or a TLS-terminating tunnel. Treat the
token like an SSH key: anyone who has it can run commands in your workspaces.
[`docs/remote-api.md`](docs/remote-api.md) documents the HTTP/JSON API for writing your own
clients.

### Notifications

The daemon can send push notifications to an [ntfy](https://ntfy.sh)-compatible webhook when a
session asks a question, finishes, errors, or gets blocked, and when the work loop finishes.
Each notification links to `ycc://session/<id>`, so tapping it on a phone with the app
installed opens the session.

```toml
[notify]
url = "https://ntfy.sh/my-private-topic"
auth_env = "NTFY_AUTH"      # optional: env var holding the Authorization header, e.g. "Bearer tk_…"
# events = ["question", "blocked", "digest"]   # optional filter; default is all kinds
```

## Configuration

### Models and roles: `ycc.toml`

ycc uses the first config file it finds: `./ycc.toml` in the workspace, then
`~/.config/ycc/ycc.toml`. You can also pass one with `--config`. The files are not merged. If
no usable config exists, the TUI's first-run wizard writes one, and `ycc login` adds models.
Most settings can also be changed live from the web or iOS settings screens, and those changes
are written back to the file.

```toml
[models.opus]                      # a logical name you choose
backend = "anthropic"
model   = "claude-opus-4-8"
auth    = "oauth"                  # use the `ycc login anthropic` subscription

[models.gpt]
backend = "openai"
model   = "gpt-5.5"
auth    = "oauth"                  # use the `ycc login openai` ChatGPT subscription

[models.glm]
backend  = "openai-compatible"
base_url = "https://api.fireworks.ai/inference/v1"
model    = "accounts/fireworks/models/glm-5p3"
key_env  = "FIREWORKS_API_KEY"     # the NAME of an env var / `ycc token` entry, never the key itself

[roles]
coordinator = "opus"               # plans, delegates, reviews, commits
implementer = "gpt"                # writes the code in work mode
reviewers   = ["gpt", "opus"]      # used by review tiers

[reviews]
default = "standard"               # built-in tiers: self-review | standard | comprehensive

[work]
implementation = "delegate"        # or "direct": the coordinator edits the code itself

[integration]                      # how finished workstreams land
mode   = "auto"                    # auto | gate | manual
verify = "go test ./..."

[budget]                           # all optional; 0 = unlimited
session_cost = 20.0
loop_cost    = 100.0
```

Models can also set reasoning (`thinking`, `effort`), pricing (`price_input`, `price_output`,
… per million tokens), and `context_window`. You can disable a model without deleting it.
Other sections cover retries, transport timeouts, session garbage collection, memory grooming,
extra writable roots (`write_roots`), and worktree bootstrap steps (`[worktree]` `copy`,
`link`, `setup`). [`spec.md` §13](spec.md#13-models-credentials-and-review-tiers) describes the
behavior in detail.

### Project docs layout: `.ycc/config.toml`

If your design documents are spread across more files than `spec.md`, declare them in a
committed `.ycc/config.toml`:

```toml
spec_path = "spec.md"
doc_globs = ["docs/*.md", "docs/design/*.md"]
```

Keep the rest of `.ycc/` (session logs and runtime state) out of git with the ignore rules
`.ycc/*` and `!.ycc/config.toml`. Keep the globs narrow so they match only normative design
docs, not `memory.md`, the backlog, generated files, or reports. `ycc spec-check` scans every
file they match.

## Credentials and secrets

- **Subscriptions**: run `ycc login anthropic|openai`. To reconnect Anthropic from the iOS app,
  go to **Settings → Provider accounts**. Tokens stay on the daemon host and refresh
  automatically.
- **API keys**: `ycc token set NAME` stores a key in the machine-local secrets store. Input is
  read without echo or from stdin, and the key is never accepted as a command-line argument.
  Config files refer to keys only by name. Keys are looked up in the daemon's environment
  first and then in the store. Stored values are never passed to tool processes.
- **Agent shells** start with known credential variables removed from their environment
  (`YCC_TOKEN`, provider keys, every configured `key_env`). This is a safety net, not a
  sandbox. A shell running as your user can still read your files.
- **Web tools**: `web_search`/`fetch_page` use [Exa](https://exa.ai). An `EXA_API_KEY` in the
  daemon's environment enables them. A key from the secrets store must be authorized each time
  it's used, and each grant covers one tool call in one workspace:
  `ycc token authorize EXA_API_KEY --tool web_search --workspace /path`.

**Never paste a credential into a prompt or an answer to an agent's question.** Session logs
are durable model history. ycc refuses input that is obviously credential-shaped and redacts
known values in exports, but those checks can't catch everything. If a secret ends up in a
session, rotate it at the provider first. Then decide whether to keep or delete that session's
`.ycc/sessions/<id>` directory. ycc never rewrites logs.

| Variable | Used for |
|----------|----------|
| `ANTHROPIC_API_KEY` | the default key name for Anthropic API-key models |
| `EXA_API_KEY` | the `web_search` / `fetch_page` tools |
| `YCC_TOKEN` | the daemon's bearer token (server and clients) |

## Development

ycc is developed with ycc, and this repository uses the same layout it asks of other projects:
[`spec.md`](spec.md), [`backlog/`](backlog), [`plans/`](plans), [`memory.md`](memory.md), and
[`CONTRIBUTING.md`](CONTRIBUTING.md).

| Path | Contents |
|------|----------|
| `cmd/ycc` | the binary: CLI commands, daemon startup |
| `internal/` | engine (agent loop), orchestrator (modes, work and review flow), server (Connect RPC), session/event log, tools, providers, workstreams, TUI, embedded web bundle |
| `proto/ycc/v1` | `ycc.proto`, the wire schema shared by every client |
| `clients/web` | the desktop web client (TypeScript, React, Vite); the bundle is committed to `internal/web/dist` |
| `clients/ios` | the iPhone app (SwiftUI shell + `YccKit` Swift package) |

CI runs Go 1.26.8 on Linux and Xcode 16.4 on macOS. Before submitting a change, run the
matching local checks. The Swift checks require macOS, and none of the checks need provider
API keys.

```sh
# Go: formatting, vet, tests (uncached), PTY end-to-end tests, race detector, docs drift
test -z "$(git ls-files '*.go' | xargs gofmt -l)"
go vet ./...
go test -count=1 ./...
go test -count=1 -v ./internal/e2e   # every TestE2E test must PASS, not SKIP
go test -race -count=1 ./...
go run ./cmd/ycc spec-check

# Vulnerability scan (the result depends on the Go patch version)
go install golang.org/x/vuln/cmd/govulncheck@v1.8.0 && govulncheck ./...

# Web client: install from the lockfile, typecheck, bundle, and run Vitest; the committed bundle must match
scripts/web-build.sh
git diff --exit-code -- internal/web/dist

# iOS (macOS only, XcodeGen 2.44.1)
swift test --package-path clients/ios/YccKit
(cd clients/ios && xcodegen generate)
xcodebuild -project clients/ios/Ycc.xcodeproj -scheme Ycc \
  -destination 'generic/platform=iOS Simulator' CODE_SIGNING_ALLOWED=NO build
```

If you change `proto/ycc/v1/ycc.proto`, regenerate the Go, Swift, and TypeScript bindings and
commit them together, following [`plans/build-and-test.md`](plans/build-and-test.md). CI checks
the result with pinned tool versions: buf v1.71.0, protoc-gen-go v1.36.11, and
protoc-gen-connect-go v1.20.0.
