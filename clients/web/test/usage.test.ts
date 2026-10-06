// Usage dashboard: URL query round-trips, the GetUsage request, CLI-identical
// cells (labels, counts, costs), price-status merging, sorting and shares, the
// gap-filled timeline, subscription windows, and budget caps.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { GetBudgetResponseSchema, SubscriptionUsageAccountSchema, SubscriptionUsageWindowSchema, UsageRowSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  DEFAULT_QUERY,
  accountBadge,
  budgetRows,
  bucketSizeFor,
  buildTimeline,
  cellLabel,
  commas,
  costCell,
  headlineCost,
  mergePriceStatus,
  parseUsageQuery,
  rangeError,
  resetText,
  resolveRange,
  rowShare,
  shortTokens,
  sortRows,
  usageRequest,
  usageSearch,
  windowTone,
} from "../src/features/usage/model";

const row = (o: Partial<{ task: string; model: string; session: string; agent: string; day: string; input: bigint; output: bigint; cacheRead: bigint; cacheWrite: bigint; total: bigint; cost: number; priceStatus: string }>) =>
  create(UsageRowSchema, { priceStatus: "priced", ...o });

const NOW = new Date("2026-10-06T15:30:00Z");

describe("usage query", () => {
  it("defaults to the daemon's task grouping over all time", () => {
    expect(parseUsageQuery("")).toEqual(DEFAULT_QUERY);
    expect(usageSearch(DEFAULT_QUERY)).toBe("");
  });

  it("round-trips grouping, presets, custom ranges, and the task filter", () => {
    const q = parseUsageQuery("?by=model,day,model,bogus&range=7d&task=0410");
    expect(q).toEqual({ by: ["model", "day"], range: "7d", since: "", until: "", task: "0410" });
    expect(parseUsageQuery(usageSearch(q))).toEqual(q);
    const custom = parseUsageQuery("?since=2026-10-01&until=2026-10-03");
    expect(custom.range).toBe("custom");
    expect(usageSearch(custom)).toBe("?since=2026-10-01&until=2026-10-03");
    // Invalid days are dropped rather than sent.
    expect(parseUsageQuery("?since=2026-02-30&until=yesterday")).toMatchObject({ since: "", until: "", range: "custom" });
  });

  it("resolves presets to inclusive UTC days", () => {
    const q = (range: string) => parseUsageQuery(`?range=${range}`);
    expect(resolveRange(q("all"), NOW)).toEqual({ since: "", until: "" });
    expect(resolveRange(q("today"), NOW)).toEqual({ since: "2026-10-06", until: "2026-10-06" });
    expect(resolveRange(q("7d"), NOW)).toEqual({ since: "2026-09-30", until: "2026-10-06" });
    expect(resolveRange(q("30d"), NOW)).toEqual({ since: "2026-09-07", until: "2026-10-06" });
    expect(resolveRange(q("month"), NOW)).toEqual({ since: "2026-10-01", until: "2026-10-06" });
  });

  it("builds the GetUsage request (with a day-grouped variant for the timeline)", () => {
    const q = parseUsageQuery("?by=task,model&range=today&task=0001");
    expect(usageRequest("gamma", q, NOW)).toEqual({ project: "gamma", groupBy: ["task", "model"], since: "2026-10-06", until: "2026-10-06", task: "0001" });
    expect(usageRequest("", q, NOW, ["day"]).groupBy).toEqual(["day"]);
  });

  it("flags a reversed custom range", () => {
    expect(rangeError(parseUsageQuery("?since=2026-10-05&until=2026-10-01"))).toMatch(/after/);
    expect(rangeError(parseUsageQuery("?since=2026-10-01&until=2026-10-05"))).toBeNull();
  });
});

describe("usage cells match `ycc cost`", () => {
  it("labels blank dimensions like the CLI", () => {
    const r = row({ session: "s1", day: "2026-10-06" });
    expect(cellLabel(r, "task")).toBe("(unattributed)");
    expect(cellLabel(r, "model")).toBe("(unknown)");
    expect(cellLabel(r, "agent")).toBe("(unknown)");
    expect(cellLabel(r, "session")).toBe("s1");
    expect(cellLabel(row({ task: "0410", model: "stub", agent: "reviewer" }), "agent")).toBe("reviewer");
  });

  it("prints counts with thousands separators and costs to four places", () => {
    expect(commas(0n)).toBe("0");
    expect(commas(999n)).toBe("999");
    expect(commas(1234n)).toBe("1,234");
    expect(commas(1234567890n)).toBe("1,234,567,890");
    expect(commas(-1234n)).toBe("-1,234");
    expect(costCell(0.01234, "priced")).toBe("$0.0123");
    expect(costCell(1.5, "partial")).toBe("$1.5000*");
    expect(costCell(2, "unpriced")).toBe("—");
    expect(headlineCost(12.345, "priced")).toBe("$12.35");
    expect(headlineCost(0.0042, "partial")).toBe("$0.0042*");
    expect(headlineCost(0, "unpriced")).toBe("Unpriced");
    expect(shortTokens(842n)).toBe("842");
    expect(shortTokens(12_345n)).toBe("12.3K");
    expect(shortTokens(1_234_567n)).toBe("1.2M");
  });

  it("merges price statuses like the daemon", () => {
    expect(mergePriceStatus(["priced", "priced"])).toBe("priced");
    expect(mergePriceStatus(["unpriced", "unpriced"])).toBe("unpriced");
    expect(mergePriceStatus(["priced", "unpriced"])).toBe("partial");
    expect(mergePriceStatus(["priced", "partial"])).toBe("partial");
  });
});

describe("breakdown ordering and shares", () => {
  const a = row({ task: "0002", total: 100n, input: 90n, output: 10n, cost: 0.5 });
  const b = row({ task: "0001", total: 300n, input: 200n, output: 100n, cost: 0.1 });
  const c = row({ task: "", total: 50n, input: 50n, priceStatus: "unpriced" });
  const total = row({ total: 450n, cost: 0.6, priceStatus: "partial" });

  it("keeps the daemon order by default and sorts on demand", () => {
    expect(sortRows([b, a, c], ["task"], "default", true)).toEqual([b, a, c]);
    expect(sortRows([b, a, c], ["task"], "cost", true).map((r) => r.task)).toEqual(["0002", "0001", ""]);
    expect(sortRows([b, a, c], ["task"], "total", false).map((r) => r.task)).toEqual(["", "0002", "0001"]);
    expect(sortRows([b, a, c], ["task"], "label", false).map((r) => cellLabel(r, "task"))).toEqual(["(unattributed)", "0001", "0002"]);
  });

  it("computes shares by tokens or by priced cost", () => {
    expect(rowShare(b, total, "tokens")).toBeCloseTo(300 / 450);
    expect(rowShare(a, total, "cost")).toBeCloseTo(0.5 / 0.6);
    expect(rowShare(c, total, "cost")).toBe(0);
    expect(rowShare(a, null, "tokens")).toBe(0);
  });
});

describe("timeline", () => {
  it("fills gaps between usage days and sums buckets", () => {
    const rows = [
      row({ day: "2026-10-01", total: 100n, cost: 1 }),
      row({ day: "2026-10-04", total: 50n, cost: 0, priceStatus: "unpriced" }),
    ];
    const t = buildTimeline(rows, { since: "", until: "" }, NOW);
    expect(t.size).toBe("day");
    expect(t.buckets.map((b) => b.start)).toEqual(["2026-10-01", "2026-10-02", "2026-10-03", "2026-10-04"]);
    expect(t.buckets[0]).toMatchObject({ tokens: 100n, cost: 1, hasUsage: true, status: "priced", label: "Oct 1" });
    expect(t.buckets[1].hasUsage).toBe(false);
    expect(t.buckets[3].status).toBe("unpriced");
  });

  it("extends to a preset's bounds but never past today", () => {
    const rows = [row({ day: "2026-10-05", total: 10n })];
    const t = buildTimeline(rows, { since: "2026-09-30", until: "2026-10-31" }, NOW);
    expect(t.buckets[0].start).toBe("2026-09-30");
    expect(t.buckets[t.buckets.length - 1].start).toBe("2026-10-06");
  });

  it("switches to weeks and months for long spans", () => {
    expect(bucketSizeFor(30)).toBe("day");
    expect(bucketSizeFor(120)).toBe("week");
    expect(bucketSizeFor(800)).toBe("month");
    const rows = [row({ day: "2026-06-03", total: 1n }), row({ day: "2026-06-05", total: 2n }), row({ day: "2026-10-06", total: 4n })];
    const t = buildTimeline(rows, { since: "", until: "" }, NOW);
    expect(t.size).toBe("week");
    // 2026-06-03 is a Wednesday: its week starts Monday 2026-06-01 and holds both June days.
    expect(t.buckets[0]).toMatchObject({ start: "2026-06-01", end: "2026-06-07", tokens: 3n });
    expect(t.buckets.reduce((s, b) => s + b.tokens, 0n)).toBe(7n);
  });
});

describe("subscription allowance and budget", () => {
  it("describes reset times and usage tone", () => {
    const now = Date.parse("2026-10-06T12:00:00Z");
    expect(resetText(0n, now)).toBeNull();
    expect(resetText(BigInt(now / 1000 + 30 * 60), now)).toBe("resets in 30m");
    expect(resetText(BigInt(now / 1000 + 2 * 3600 + 14 * 60), now)).toBe("resets in 2h 14m");
    expect(resetText(BigInt(now / 1000 + 3 * 86400), now)).toMatch(/^resets Oct 9, \d\d:\d\d$/);
    const w = (p: number) => create(SubscriptionUsageWindowSchema, { usedPercent: p });
    expect(windowTone(w(40))).toBe("ok");
    expect(windowTone(w(92))).toBe("warn");
    expect(windowTone(w(100))).toBe("over");
    expect(accountBadge(create(SubscriptionUsageAccountSchema, { state: "fresh" }))).toBeNull();
    expect(accountBadge(create(SubscriptionUsageAccountSchema, { state: "stale" }))).toEqual({ label: "stale", tone: "warn" });
  });

  it("shows unlimited caps for zero", () => {
    const b = create(GetBudgetResponseSchema, { sessionCost: 5, sessionTokens: 0n, loopCost: 0, loopTokens: 2_000_000n });
    expect(budgetRows(b).map((r) => r.value)).toEqual(["$5.00", "Unlimited", "Unlimited", "2,000,000 tokens"]);
  });
});
