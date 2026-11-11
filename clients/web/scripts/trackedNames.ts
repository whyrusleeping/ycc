// The analytics catalog of the web client: every action and view name written
// as a literal in the sources (docs/design/usage-analytics.md#catalog). Vite
// defines it as __YCC_TRACKED__ (vite.config.ts), so a newly instrumented
// control is listed — and reported when never used — without a hand-kept list.
// Names built at run time (template literals) are not seen here; register
// those with track.catalog.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

export interface TrackedNames {
  actions: string[];
  views: string[];
}

// The daemon's token charset and name bound.
const NAME = /^[A-Za-z0-9_.:/-]{1,80}$/;
// A flow records these around its form (analytics.ts openFlow/submitFlow).
const FLOW_STEPS = ["open", "submit", "cancel"];

/** The plain string literals in a JS expression (template literals, and strings inside them, skipped). */
function literals(expr: string): string[] {
  return [...expr.replace(/`[^`]*`/g, "").matchAll(/"([^"\\]*)"|'([^'\\]*)'/g)].map((m) => m[1] ?? m[2]);
}

/** The literals of a JSX attribute's value: `attr="x"` or `attr={a ? "x" : "y"}`. */
function attrValues(src: string, attr: string): string[] {
  const re = new RegExp(`\\s${attr}=(?:"([^"]*)"|\\{([^{}]*)\\})`, "g");
  return [...src.matchAll(re)].flatMap((m) => (m[1] !== undefined ? [m[1]] : literals(m[2])));
}

/** The literals of a call's first argument: `fn("x"` or `fn(a ? "x" : "y",`. */
function firstArgs(src: string, fn: string): string[] {
  const re = new RegExp(`${fn.replace(/\./g, "\\.")}\\(\\s*([^,()]*)[,)]`, "g");
  return [...src.matchAll(re)].flatMap((m) => literals(m[1]));
}

/** The names one source file can record. */
export function namesInSource(src: string): TrackedNames {
  const flows = [
    ...firstArgs(src, "useFlow"),
    ...firstArgs(src, "track.submit"),
    ...attrValues(src, "flow"),
    // Flow descriptors handed to a Modal (the provider logins).
    ...[...src.matchAll(/\bflow:\s*"([^"]+)"/g)].map((m) => m[1]),
  ];
  const actions = [
    ...attrValues(src, "data-track"),
    ...firstArgs(src, "track.action"),
    ...flows.flatMap((f) => FLOW_STEPS.map((s) => `${f}.${s}`)),
    // ConfirmDialog records <action>.confirm / <action>.cancel.
    ...attrValues(src, "action").flatMap((a) => [`${a}.confirm`, `${a}.cancel`]),
    // CopyButton records copy.<what>.
    ...attrValues(src, "what").map((w) => `copy.${w}`),
    // Menu items and registered app actions record their (dotted) id; doc.*
    // ids only document keys in the shortcut help and are never recorded.
    ...[...src.matchAll(/\bid:\s*["']([a-z][A-Za-z0-9_]*\.[A-Za-z0-9_.]+)["']/g)].map((m) => m[1]).filter((id) => !id.startsWith("doc.")),
  ];
  const views = [...attrValues(src, "view"), ...firstArgs(src, "useTrackedView")];
  return { actions, views };
}

function sourceFiles(dir: string, out: string[] = []): string[] {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, e.name);
    if (e.isDirectory()) {
      if (e.name !== "gen") sourceFiles(path, out);
    } else if (/\.tsx?$/.test(e.name) && !e.name.endsWith(".d.ts")) out.push(path);
  }
  return out;
}

/** Every literal action and view name under `srcDir`, sorted and deduplicated. */
export function trackedNames(srcDir: string): TrackedNames {
  const actions = new Set<string>();
  const views = new Set<string>();
  for (const file of sourceFiles(srcDir)) {
    const found = namesInSource(readFileSync(file, "utf8"));
    for (const a of found.actions) if (NAME.test(a)) actions.add(a);
    for (const v of found.views) if (NAME.test(v)) views.add(v);
  }
  return { actions: [...actions].sort(), views: [...views].sort() };
}
