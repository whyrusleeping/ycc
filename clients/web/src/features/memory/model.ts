// memory.md parsed into sections of typed notes: a port of the daemon's
// internal/docs parseMemory, so the viewer can show each note's model-chosen
// classification, runtime provenance, id, and whether it is still active
// (superseded and retired records stay in the file as an audit trail but no
// longer reach prompts).

export type MemoryKind = "user_guidance" | "observation" | "inference" | "proposed_policy" | "retraction";

export const KIND_LABELS: Record<MemoryKind, string> = {
  user_guidance: "user-stated guidance",
  observation: "measured observation",
  inference: "model inference",
  proposed_policy: "proposed policy",
  retraction: "retraction",
};

export type NoteStatus =
  /** Renders into every agent prompt. */
  | "active"
  /** Corrected by a later note (`supersededBy`). */
  | "superseded"
  /** Retired without replacement by a retraction record (`supersededBy`). */
  | "retired"
  /** A retraction record itself (audit only). */
  | "retraction";

export interface MemoryNote {
  /** The section heading without `## `. */
  section: string;
  id: string;
  /** null for legacy (untyped) bullets. */
  kind: MemoryKind | null;
  note: string;
  /** Recorded date (YYYY-MM-DD); "" for legacy bullets. */
  date: string;
  session: string;
  event: number;
  actor: string;
  scope: string;
  supersedes: string[];
  legacy: boolean;
  status: NoteStatus;
  /** The note(s) that superseded or retired this one. */
  supersededBy: string[];
}

export interface MemorySection {
  title: string;
  notes: MemoryNote[];
}

export interface ParsedMemory {
  /** The `#` title and `>` preamble lines (without the markers). */
  title: string;
  preamble: string[];
  sections: MemorySection[];
  notes: MemoryNote[];
  counts: Record<NoteStatus, number>;
}

const META_MARKER = " <!-- ycc-memory ";

function queryUnescape(v: string): string | null {
  try {
    return decodeURIComponent(v.replace(/\+/g, " "));
  } catch {
    return null;
  }
}

export function parseMemory(body: string): ParsedMemory {
  const notes: MemoryNote[] = [];
  const sections: MemorySection[] = [];
  const preamble: string[] = [];
  let title = "";
  let section = "## Uncategorised";
  let current: MemorySection | null = null;
  for (const raw of body.split("\n")) {
    const line = raw.trim();
    if (line.startsWith("## ")) {
      section = line;
      current = null;
      continue;
    }
    if (!line.startsWith("- ")) {
      if (!sections.length && !current) {
        if (line.startsWith("# ") && !title) title = line.slice(2).trim();
        else if (line.startsWith(">")) preamble.push(line.replace(/^>\s?/, ""));
      }
      continue;
    }
    const note: MemoryNote = {
      section: section.slice(3).trim(),
      id: "",
      kind: null,
      note: "",
      date: "",
      session: "",
      event: 0,
      actor: "",
      scope: "workspace",
      supersedes: [],
      legacy: false,
      status: "active",
      supersededBy: [],
    };
    const marker = line.lastIndexOf(META_MARKER);
    const legacy = () => {
      note.legacy = true;
      note.kind = null;
      note.note = (marker >= 0 && line.endsWith(" -->") ? line.slice(0, marker) : line).trim().replace(/^- /, "").trim();
      note.id = legacyMemoryId(section, line);
    };
    if (marker < 0 || !line.endsWith(" -->")) {
      legacy();
    } else {
      const visible = line.slice(0, marker).trim();
      const metaText = line.slice(marker + META_MARKER.length, line.length - " -->".length);
      const meta = new Map<string, string>();
      for (const field of metaText.split(/\s+/)) {
        const eq = field.indexOf("=");
        if (eq < 0) continue;
        const v = queryUnescape(field.slice(eq + 1));
        if (v !== null) meta.set(field.slice(0, eq), v);
      }
      note.id = meta.get("id") ?? "";
      const kind = meta.get("kind") ?? "";
      note.session = meta.get("session") ?? "";
      note.event = Number.parseInt(meta.get("event") ?? "", 10) || 0;
      note.actor = meta.get("actor") ?? "";
      if (meta.get("scope")) note.scope = meta.get("scope")!;
      note.supersedes = (meta.get("supersedes") ?? "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const rest = visible.replace(/^- /, "");
      const sp = rest.indexOf(" ");
      if (sp > 0) {
        note.date = rest.slice(0, sp);
        const after = rest.slice(sp + 1);
        const end = after.indexOf("] ");
        if (after.startsWith("[") && end >= 0) note.note = after.slice(end + 2).trim();
      }
      if (Object.hasOwn(KIND_LABELS, kind)) note.kind = kind as MemoryKind;
      if (!note.id || !note.note || !note.kind) {
        note.date = "";
        legacy();
      }
    }
    notes.push(note);
    if (!current) {
      current = { title: note.section, notes: [] };
      sections.push(current);
    }
    current.notes.push(note);
  }
  // Statuses: a note named by a later record's supersedes is inactive.
  const by = new Map<string, MemoryNote[]>();
  for (const n of notes) for (const id of n.supersedes) by.set(id, [...(by.get(id) ?? []), n]);
  const counts: Record<NoteStatus, number> = { active: 0, superseded: 0, retired: 0, retraction: 0 };
  for (const n of notes) {
    const refs = by.get(n.id) ?? [];
    n.supersededBy = refs.map((r) => r.id);
    if (n.kind === "retraction") n.status = "retraction";
    else if (refs.length) n.status = refs.some((r) => r.kind !== "retraction") ? "superseded" : "retired";
    counts[n.status]++;
  }
  return { title, preamble, sections, notes, counts };
}

/** The runtime provenance line, as the prompt renders it: `s_x#12/actor`. */
export function provenance(n: MemoryNote): string {
  if (n.legacy) return "";
  if (!n.session || n.event <= 0) return "no evidence";
  let out = `${n.session}#${n.event}`;
  if (n.actor && n.actor !== "coordinator") out += `/${n.actor}`;
  return out;
}

/** Notes shown under a filter: inactive ones only when asked, and a case-insensitive text match. */
export function filterNotes(notes: readonly MemoryNote[], opts: { showInactive: boolean; query: string }): MemoryNote[] {
  const q = opts.query.trim().toLowerCase();
  return notes.filter((n) => {
    if (!opts.showInactive && n.status !== "active") return false;
    if (!q) return true;
    return (
      n.note.toLowerCase().includes(q) ||
      n.id.toLowerCase().includes(q) ||
      n.session.toLowerCase().includes(q) ||
      (n.kind ? KIND_LABELS[n.kind].includes(q) : "legacy".includes(q))
    );
  });
}

/** Group notes by section, keeping the file's order and dropping empty sections. */
export function groupBySection(notes: readonly MemoryNote[]): MemorySection[] {
  const out: MemorySection[] = [];
  for (const n of notes) {
    const last = out[out.length - 1];
    if (last && last.title === n.section) last.notes.push(n);
    else {
      const existing = out.find((s) => s.title === n.section);
      if (existing) existing.notes.push(n);
      else out.push({ title: n.section, notes: [n] });
    }
  }
  return out;
}

/** The daemon's id for an untyped bullet: `legacy-` + 6 bytes of sha256(section + "\n" + line). */
export function legacyMemoryId(section: string, line: string): string {
  return `legacy-${sha256Hex(`${section}\n${line.trim()}`).slice(0, 12)}`;
}

export function kb(bytes: number): string {
  return `${(bytes / 1024).toFixed(1)} KB`;
}

// SHA-256 over UTF-8 (synchronous; crypto.subtle needs a secure context, which
// a daemon reached over plain http is not).
const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01,
  0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
  0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da, 0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147,
  0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070, 0x19a4c116, 0x1e376c08,
  0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
  0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

export function sha256Hex(text: string): string {
  const msg = new TextEncoder().encode(text);
  const bitLen = msg.length * 8;
  const padded = new Uint8Array(((msg.length + 9 + 63) >> 6) << 6);
  padded.set(msg);
  padded[msg.length] = 0x80;
  const view = new DataView(padded.buffer);
  view.setUint32(padded.length - 8, Math.floor(bitLen / 0x100000000));
  view.setUint32(padded.length - 4, bitLen >>> 0);
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
  const w = new Uint32Array(64);
  const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < padded.length; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4);
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^ (w[i - 15] >>> 3);
      const s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^ (w[i - 2] >>> 10);
      w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, hh] = h;
    for (let i = 0; i < 64; i++) {
      const S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
      const ch = (e & f) ^ (~e & g);
      const t1 = (hh + S1 + ch + K[i] + w[i]) >>> 0;
      const S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
      const maj = (a & b) ^ (a & c) ^ (b & c);
      const t2 = (S0 + maj) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0] + a) >>> 0;
    h[1] = (h[1] + b) >>> 0;
    h[2] = (h[2] + c) >>> 0;
    h[3] = (h[3] + d) >>> 0;
    h[4] = (h[4] + e) >>> 0;
    h[5] = (h[5] + f) >>> 0;
    h[6] = (h[6] + g) >>> 0;
    h[7] = (h[7] + hh) >>> 0;
  }
  return Array.from(h, (x) => x.toString(16).padStart(8, "0")).join("");
}
