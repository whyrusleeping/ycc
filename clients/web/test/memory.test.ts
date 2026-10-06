// memory.md parsing (internal/docs parseMemory parity): typed records with
// provenance, legacy bullets and their ids, supersession and retirement,
// grouping, and filtering.
import { describe, expect, it } from "vitest";
import {
  filterNotes,
  groupBySection,
  kb,
  legacyMemoryId,
  parseMemory,
  provenance,
  sha256Hex,
} from "../src/features/memory/model";

const MEMORY = [
  "# Project memory",
  "",
  "> Agent-maintained operational notes. Advisory, not normative.",
  "> Design truth belongs in spec.md.",
  "",
  "## Codebase gotchas",
  "",
  "- Manager goroutines running git must be joinable (ReclaimAll waits) or TempDir tests race; server.workstreamError maps errors BY STRING.",
  "- 2026-10-04 [model inference] Engine: `internal/engine/loop.go` is subtle. <!-- ycc-memory id=m-old kind=inference session=s_abc event=97 actor=coordinator scope=workspace classified=model -->",
  "- 2026-10-05 [model inference] Engine (corrected). <!-- ycc-memory id=m-new kind=inference session=s_abc event=120 actor=implementer scope=workspace classified=model supersedes=m-old -->",
  "",
  "## User preferences",
  "",
  "- 2026-09-28 [user-stated guidance] Never block cheap user actions. <!-- ycc-memory id=m-pref kind=user_guidance session=s_fbe event=2 actor=user scope=workspace classified=model -->",
  "- 2026-09-29 [measured observation] Uses a %2B plus. <!-- ycc-memory id=m-obs kind=observation session= event=0 actor= scope=global classified=model -->",
  "- 2026-09-30 [weird] Unknown kind falls back. <!-- ycc-memory id=m-weird kind=bogus session=s event=1 -->",
  "",
  "## Retired notes (audit only)",
  "- 2026-10-04 [retired] stale <!-- ycc-memory id=m-ret kind=retraction session=s_6de event=41 actor=coordinator scope=workspace classified=model supersedes=m-pref -->",
  "",
].join("\n");

describe("memory parsing", () => {
  const m = parseMemory(MEMORY);
  const byId = (id: string) => m.notes.find((n) => n.id === id)!;

  it("reads the title, preamble, and sections in order", () => {
    expect(m.title).toBe("Project memory");
    expect(m.preamble).toEqual(["Agent-maintained operational notes. Advisory, not normative.", "Design truth belongs in spec.md."]);
    expect(m.sections.map((s) => [s.title, s.notes.length])).toEqual([
      ["Codebase gotchas", 3],
      ["User preferences", 3],
      ["Retired notes (audit only)", 1],
    ]);
  });

  it("parses typed records with provenance", () => {
    const n = byId("m-new");
    expect(n).toMatchObject({
      kind: "inference",
      date: "2026-10-05",
      note: "Engine (corrected).",
      session: "s_abc",
      event: 120,
      actor: "implementer",
      supersedes: ["m-old"],
      status: "active",
      legacy: false,
    });
    expect(provenance(n)).toBe("s_abc#120/implementer");
    expect(provenance(byId("m-old"))).toBe("s_abc#97");
    expect(byId("m-obs")).toMatchObject({ note: "Uses a %2B plus.", scope: "global" });
    expect(provenance(byId("m-obs"))).toBe("no evidence");
  });

  it("gives legacy bullets the daemon's id", () => {
    const legacy = m.notes[0];
    expect(legacy.legacy).toBe(true);
    expect(legacy.kind).toBeNull();
    // Same id the daemon renders into prompts (memory.md of this repo).
    expect(legacy.id).toBe("legacy-608bdeb06db1");
    expect(provenance(legacy)).toBe("");
    // An unknown kind is untyped too, without its metadata in the text.
    const weird = m.notes.find((n) => n.note.includes("Unknown kind"))!;
    expect(weird.legacy).toBe(true);
    expect(weird.note).toBe("2026-09-30 [weird] Unknown kind falls back.");
    expect(weird.id).toMatch(/^legacy-[0-9a-f]{12}$/);
  });

  it("marks superseded, retired, and retraction records", () => {
    expect(byId("m-old")).toMatchObject({ status: "superseded", supersededBy: ["m-new"] });
    expect(byId("m-pref")).toMatchObject({ status: "retired", supersededBy: ["m-ret"] });
    expect(byId("m-ret").status).toBe("retraction");
    expect(m.counts).toEqual({ active: 4, superseded: 1, retired: 1, retraction: 1 });
  });

  it("filters to active notes by default and matches text, ids and kinds", () => {
    const active = filterNotes(m.notes, { showInactive: false, query: "" });
    expect(active.map((n) => n.id)).not.toContain("m-old");
    expect(active).toHaveLength(4);
    expect(filterNotes(m.notes, { showInactive: true, query: "" })).toHaveLength(7);
    expect(filterNotes(m.notes, { showInactive: true, query: "ENGINE" }).map((n) => n.id)).toEqual(["m-old", "m-new"]);
    expect(filterNotes(m.notes, { showInactive: false, query: "m-pref" })).toEqual([]);
    expect(filterNotes(m.notes, { showInactive: false, query: "observation" }).map((n) => n.id)).toEqual(["m-obs"]);
    expect(filterNotes(m.notes, { showInactive: false, query: "legacy" })).toHaveLength(2);
  });

  it("groups filtered notes by section, dropping empty sections", () => {
    const groups = groupBySection(filterNotes(m.notes, { showInactive: false, query: "" }));
    expect(groups.map((g) => [g.title, g.notes.map((n) => n.id)])).toEqual([
      ["Codebase gotchas", ["legacy-608bdeb06db1", "m-new"]],
      ["User preferences", ["m-obs", expect.stringMatching(/^legacy-/)]],
    ]);
  });

  it("handles an empty file", () => {
    const e = parseMemory("");
    expect(e.notes).toEqual([]);
    expect(e.sections).toEqual([]);
  });
});

describe("helpers", () => {
  it("sha256 matches known vectors", () => {
    expect(sha256Hex("")).toBe("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855");
    expect(sha256Hex("abc")).toBe("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    expect(sha256Hex("a".repeat(1000))).toBe("41edece42d63e8d9bf515a9ba6932e1c20cbc9f5a5d134645adb5db1b9737ea3");
    expect(sha256Hex("héllo ✓")).toBe("5657cdef8a85a584e0e961e6f8247cf5d3f8ed21496ed6fdbcfd43a761e94245");
    expect(legacyMemoryId("## A", "  - x  ")).toBe(`legacy-${sha256Hex("## A\n- x").slice(0, 12)}`);
  });

  it("formats kilobytes like iOS", () => {
    expect(kb(1536)).toBe("1.5 KB");
    expect(kb(0)).toBe("0.0 KB");
  });
});
