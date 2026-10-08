# Client usage analytics

Goal: learn which parts of the clients the user actually uses — and where they struggle — so
improvement work can be aimed at real usage. Analytics are private, local, and owned by the
daemon; there is no third-party service.

## Flow

```
web / iOS / TUI ──RecordUiEvents (batched, best-effort)──▶ daemon ──▶ <state>/ycc/analytics/
                                                                         events-YYYY-MM.jsonl
ycc analytics / GetUiAnalytics ◀── summarised report ◀───────────────── catalog.json
```

- Clients buffer events in memory and flush every ~30s, when hidden/backgrounded, and when the
  buffer reaches 100 events. A failed flush is dropped silently; analytics never surface errors,
  block UI, or retry aggressively. Recording must stay a few microseconds per event.
- Only a persistent daemon stores events (owner-only files, retained 12 months). The one-shot
  in-process daemon accepts and discards them (`stored=false`).
- `ycc analytics [--days N] [--client web|ios|tui] [--json]` prints the summary. It uses a reachable
  daemon (or `--addr`) and otherwise reads the local state directory directly. The report names
  the raw JSONL directory so an agent can dig deeper with grep/jq.

## Privacy rule

Events carry **identifiers from a fixed vocabulary only**: screen names, action ids, input
methods, error codes, coarse enums. Never prompts, task titles, file paths, project/session ids,
search text, or any user content. The daemon enforces a charset (`[A-Za-z0-9_.:/-]`, no spaces) and
length bounds, dropping events that fail; that makes accidental prose leakage hard but is not a
substitute for clients following this rule.

## Event schema (`UiEvent`)

| field         | meaning |
|---------------|---------|
| `time_ms`     | client wall clock, unix ms (clamped by the daemon to a sane window) |
| `kind`        | `visit`, `view`, `action`, or `error` |
| `name`        | stable snake/dotted id, ≤80 chars |
| `view`        | the screen the event happened on (for `action`/`error`) |
| `via`         | how an action was invoked (see below) |
| `duration_ms` | `view`: visible dwell time; `action`: optional operation latency |
| `attrs`       | ≤8 enum-valued key/values, e.g. `from=backlog`, `code=unavailable`, `outcome=cancel` |

Request-level fields: `client` (`web`/`ios`/`tui`), `client_version`, `visit_id` (random per page
load / app foreground; groups events into visits), and an optional `catalog`.

### Kinds

- **`visit`** — a page load or app foreground. `name` = `start`. Useful attrs: `layout`
  (`narrow`/`wide`), `theme`, `input` (`touch`/`mouse`).
- **`view`** — a screen or modal surface was shown. Emitted when it is *left* (navigation, close,
  hide/background) so `duration_ms` is the visible dwell; `attrs.from` is the previous view. The
  daemon derives navigation paths (`from → name`) from these.
- **`action`** — the user did something. `name` is `<area>.<verb>` (`session.interrupt`,
  `backlog.promote`, `composer.attach_image`). `via` is one of `shortcut`, `palette`, `click`,
  `menu`, `context_menu`, `swipe`, `gesture`, `keyboard`, `link`, `auto`. Flows that can be
  abandoned record their start and end (`new_session.open` … `new_session.submit` or
  `new_session.cancel`), so drop-off is visible.
- **`error`** — a user-visible failure (toast, banner, inline error). `name` = the operation
  (`backlog.update`), `attrs.code` = Connect code or a short enum.

### Shared view names

Clients use the same names for equivalent screens so usage can be compared across clients:
`home`, `new_session`, `session`, `settings`, `project`, `projects`, `backlog`, `task`, `workloop`,
`workstreams`, `files`, `file`, `memory`, `plans`, `usage`, `session_usage`, `session_settings`,
`quick_capture`, `palette`, `help`, `drawer`, `connect`. Add new names freely; keep them stable.

### Catalog

A client may send `catalog` entries (`kind`, `name`, optional `shortcut` label) naming everything
it *could* record — registered actions, screens. The daemon keeps the latest catalog per client and
the report lists catalog entries with zero recorded use in the window ("never used"), plus
actions that have a shortcut but are mostly invoked another way ("shortcut not learned").

## Report contents

Per client: visits, active days, versions seen; views by count and total/median dwell; actions by
count with `via` breakdown and the views they happen on; top navigation paths; errors by code;
abandoned flows (`*.open` without a matching `*.submit`); never-used catalog entries.
