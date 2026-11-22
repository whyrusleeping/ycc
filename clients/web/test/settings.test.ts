// Session settings: thinking levels per model, role-model choices, and the
// session-scoped usage breakdown.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ModelInfoSchema, UsageRowSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  clampThinking,
  formatCost,
  levelChoices,
  levelsAnyOf,
  levelsNamed,
  parseThinking,
  roleModelChoices,
  sessionUsage,
  supportedLevels,
} from "../src/features/session/settings";

const ALL = ["off", "low", "medium", "high", "xhigh", "max"];

describe("session settings", () => {
  it("parses levels with a medium fallback", () => {
    expect(parseThinking("XHIGH")).toBe("xhigh");
    expect(parseThinking("")).toBe("medium");
    expect(parseThinking("bogus")).toBe("medium");
  });

  it("offers a model's reported levels in scale order, or every level when none are known", () => {
    const models = [
      create(ModelInfoSchema, { name: "gpt", thinkingLevels: ["xhigh", "low", "medium", "high", "bogus"] }),
      create(ModelInfoSchema, { name: "local", thinkingLevels: ["off", "high"] }),
      create(ModelInfoSchema, { name: "old" }),
    ];
    expect(supportedLevels(models[0])).toEqual(["low", "medium", "high", "xhigh"]);
    expect(supportedLevels(undefined)).toEqual(ALL);
    expect(levelsNamed("old", models)).toEqual(ALL);
    expect(levelsNamed("missing", models)).toEqual(ALL);
    expect(levelsAnyOf(["gpt", "local"], models)).toEqual(["off", "low", "medium", "high", "xhigh"]);
    expect(levelsAnyOf([], models)).toEqual(ALL);
    // A saved level outside the list stays visible rather than being rewritten.
    expect(levelChoices(["off", "high"], "max")).toEqual(["off", "high", "max"]);
    expect(levelChoices(["off", "high"], "")).toEqual(["off", "high"]);
  });

  it("clamps a picked level like the daemon (spec 7.4)", () => {
    expect(clampThinking("high", ["low", "medium", "high"])).toBe("high");
    expect(clampThinking("off", ["low", "medium", "high", "xhigh"])).toBe("low");
    expect(clampThinking("max", ["low", "medium", "high", "xhigh"])).toBe("xhigh");
    expect(clampThinking("low", ["off", "high"])).toBe("high");
    // Ties go to the higher level; off is never a non-off level's target.
    expect(clampThinking("medium", ["off", "low", "high"])).toBe("high");
    expect(clampThinking("high", ["off"])).toBe("off");
    // Model default, unknown values and an unknown list are kept.
    expect(clampThinking("", ["low"])).toBe("");
    expect(clampThinking("adaptive", ["low"])).toBe("adaptive");
    expect(clampThinking("max", [])).toBe("max");
  });

  it("offers enabled models plus a disabled one the role holds", () => {
    const models = [
      create(ModelInfoSchema, { name: "a" }),
      create(ModelInfoSchema, { name: "b", disabled: true }),
      create(ModelInfoSchema, { name: "c", disabled: true }),
    ];
    expect(roleModelChoices(models, ["b"]).map((m) => m.name)).toEqual(["a", "b"]);
  });

  it("keeps one session's rows and merges price status like the daemon", () => {
    const row = (session: string, model: string, cost: number, priceStatus: string) =>
      create(UsageRowSchema, { session, model, input: 100n, output: 10n, cacheRead: 5n, cacheWrite: 1n, total: 116n, cost, priceStatus });
    const all = [row("s1", "a", 0.5, "priced"), row("s2", "a", 9, "priced"), row("s1", "b", 0, "unpriced")];
    const { rows, total } = sessionUsage(all, "s1");
    expect(rows.map((r) => r.model)).toEqual(["a", "b"]);
    expect(total).toEqual({ input: 200n, output: 20n, cacheRead: 10n, cacheWrite: 2n, total: 232n, cost: 0.5, priceStatus: "partial" });
    expect(sessionUsage(all, "s3")).toEqual({ rows: [], total: null });
    expect(sessionUsage([row("s1", "a", 0, "unpriced")], "s1").total?.priceStatus).toBe("unpriced");
    expect(sessionUsage([row("s1", "a", 1, "priced")], "s1").total?.priceStatus).toBe("priced");
  });

  it("formats costs, never inventing one for unpriced usage", () => {
    expect(formatCost(1.234, "priced")).toBe("$1.23");
    expect(formatCost(0.0012, "priced")).toBe("$0.0012");
    expect(formatCost(0, "unpriced")).toBe("—");
    expect(formatCost(2, "partial")).toBe("$2.00+");
  });
});
