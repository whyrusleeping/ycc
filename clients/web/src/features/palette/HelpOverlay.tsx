// The keyboard shortcuts overlay (`?`): every registered action's shortcut
// plus the keys surfaces handle themselves, grouped.
import { shortcutLabel, useActions } from "../../app/actions";
import { contextShortcuts } from "../../app/shortcuts";
import { Modal } from "../../ui/Modal";
import { closeHelp, useOverlays } from "./state";

interface Row {
  id: string;
  title: string;
  group: string;
  keys: string[];
}

const GROUP_ORDER = ["General", "Navigation", "Session", "Sessions", "Backlog", "Command palette"];

export function helpRows(actions: ReturnType<typeof useActions>): { group: string; rows: Row[] }[] {
  const rows: Row[] = [];
  const seen = new Set<string>();
  for (const a of actions) {
    if (!a.shortcut) continue;
    seen.add(a.id);
    rows.push({ id: a.id, title: a.title, group: a.group ?? "General", keys: [shortcutLabel(a.shortcut)] });
  }
  for (const d of contextShortcuts()) if (!seen.has(d.id)) rows.push(d);
  const groups = new Map<string, Row[]>();
  for (const r of rows) groups.set(r.group, [...(groups.get(r.group) ?? []), r]);
  const rank = (g: string) => (GROUP_ORDER.includes(g) ? GROUP_ORDER.indexOf(g) : GROUP_ORDER.length);
  return [...groups.entries()]
    .sort((a, b) => rank(a[0]) - rank(b[0]) || a[0].localeCompare(b[0]))
    .map(([group, rs]) => ({ group, rows: rs }));
}

export function HelpOverlay() {
  const { help } = useOverlays();
  const actions = useActions();
  const groups = helpRows(actions);
  return (
    <Modal open={help} onClose={closeHelp} title="Keyboard shortcuts" className="help-overlay" view="help">
      <p className="muted small">
        Plain keys don’t fire while you type in a text field; chords (Ctrl/⌘, Alt/⌥) do, except Alt/⌥ chords on macOS, where Option types
        characters. Every action is also in the command palette.
      </p>
      <div className="help-grid">
        {groups.map((g) => (
          <section key={g.group} className="help-group" aria-label={g.group}>
            <h3>{g.group}</h3>
            <dl>
              {g.rows.map((r) => (
                <div key={r.id} className="help-row">
                  <dt>{r.title}</dt>
                  <dd>
                    {r.keys.map((k, i) => (
                      <kbd key={i}>{k}</kbd>
                    ))}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Modal>
  );
}
