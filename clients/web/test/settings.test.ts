// Session settings: reasoning scopes, role-model choices, and the
// session-scoped usage breakdown.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ModelInfoSchema, UsageRowSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  formatCost,
  needsThinkingApply,
  parseThinking,
  roleModelChoices,
  sessionUsage,
  thinkingFor,
  thinkingRoleWire,
  withThinking,
} from "../src/features/session/settings";

const levels = { coordinator: "high", implementer: "low", reviewers: "high" } as const;

describe("session settings", () => {
  it("parses levels with a medium fallback and maps scopes to wire roles", () => {
    expect(parseThinking("XHIGH")).toBe("xhigh");
    expect(parseThinking("")).toBe("medium");
    expect(parseThinking("bogus")).toBe("medium");
    expect(thinkingRoleWire("all")).toBe("");
    expect(thinkingRoleWire("reviewers")).toBe("reviewers");
  });

  it("only applies real changes; 'all' unifies divergent roles", () => {
    expect(thinkingFor("all", levels)).toBe("high");
    expect(thinkingFor("implementer", levels)).toBe("low");
    expect(needsThinkingApply("coordinator", "high", levels)).toBe(false);
    expect(needsThinkingApply("implementer", "high", levels)).toBe(true);
    expect(needsThinkingApply("all", "high", levels)).toBe(true);
    expect(needsThinkingApply("all", "high", { coordinator: "high", implementer: "high", reviewers: "high" })).toBe(false);
    expect(withThinking("all", "off", levels)).toEqual({ coordinator: "off", implementer: "off", reviewers: "off" });
    expect(withThinking("reviewers", "max", levels)).toEqual({ ...levels, reviewers: "max" });
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
