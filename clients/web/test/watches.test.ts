// Watch logic: status/schedule labels, ordering, polling, and the editor draft
// round trip to a WatchDefinition.
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { WatchDefinitionSchema, WatchReportSchema, WatchSchema, type Watch } from "../src/gen/ycc/v1/ycc_pb";
import {
  buildDefinition,
  draftError,
  draftFromWatch,
  emptyDraft,
  formatInterval,
  fromLocalInput,
  kindNote,
  parkedLabel,
  pollInterval,
  reportLabel,
  reportTone,
  scheduleState,
  sortWatches,
  toLocalInput,
  untilTime,
} from "../src/features/watches/model";
import { paths, sectionPath } from "../src/app/paths";
import { routeView } from "../src/app/analytics";

const NOW = Date.parse("2026-11-12T12:00:00Z");

const watch = (name: string, extra: MessageInitShape<typeof WatchSchema> = {}): Watch => {
  const init: MessageInitShape<typeof WatchSchema> = { project: "alpha", definition: create(WatchDefinitionSchema, { name, brief: "check it", intervalSecs: 1800n }) };
  return create(WatchSchema, { ...init, ...extra } as MessageInitShape<typeof WatchSchema>);
};
const report = (extra: MessageInitShape<typeof WatchReportSchema>) => create(WatchReportSchema, { at: "2026-11-12T11:50:00Z", ...extra });

describe("labels", () => {
  it("labels a report by status, else by kind; failed reads as warn", () => {
    expect(reportLabel(undefined)).toBe("no runs yet");
    expect([reportLabel(report({ kind: "run", status: "alert" })), reportTone(report({ kind: "run", status: "alert" }))]).toEqual(["alert", "alert"]);
    expect([reportLabel(report({ kind: "probe", status: "ok" })), reportTone(report({ kind: "probe", status: "ok" }))]).toEqual(["ok", "ok"]);
    expect([reportLabel(report({ kind: "failed", status: "warn" })), reportTone(report({ kind: "failed" }))]).toEqual(["failed", "warn"]);
    expect([reportLabel(report({ kind: "skipped" })), reportTone(report({ kind: "skipped" }))]).toEqual(["skipped", "muted"]);
  });

  it("describes the schedule: paused, expired, running, next due", () => {
    expect(scheduleState(watch("a", { paused: true, pausedReason: "user" }), NOW).label).toBe("paused");
    expect(scheduleState(watch("a", { paused: true, pausedReason: "expired" }), NOW).label).toBe("expired");
    expect(scheduleState(watch("a", { running: true, nextDue: "2026-11-12T12:30:00Z" }), NOW)).toEqual({ label: "running", tone: "ok", running: true });
    expect(scheduleState(watch("a", { nextDue: "2026-11-12T12:30:00Z" }), NOW).label).toBe("next in 30m");
    expect(scheduleState(watch("a", { nextDue: "2026-11-12T11:00:00Z" }), NOW).label).toBe("next due now");
    expect(untilTime("2026-11-15T12:00:00Z", NOW)).toBe("in 3d");
    expect(untilTime("bogus", NOW)).toBe("");
  });

  it("formats intervals and counts waiting questions", () => {
    expect(formatInterval(300n)).toBe("5m");
    expect(formatInterval(5400)).toBe("1h 30m");
    expect(formatInterval(86400 + 6 * 3600)).toBe("1d 6h");
    expect(formatInterval(0)).toBe("—");
    expect(parkedLabel(watch("a"))).toBe("");
    expect(parkedLabel(watch("a", { parkedSessionIds: ["s1"] }))).toBe("1 question waiting");
    expect(parkedLabel(watch("a", { parkedSessionIds: ["s1", "s2"] }))).toBe("2 questions waiting");
  });
});

describe("list", () => {
  it("puts waiting questions and alerts first and paused watches last", () => {
    const list = [
      watch("ok", { last: report({ kind: "probe", status: "ok" }) }),
      watch("paused-alert", { paused: true, last: report({ kind: "run", status: "alert" }) }),
      watch("new"),
      watch("warn", { last: report({ kind: "failed" }) }),
      watch("alert", { last: report({ kind: "run", status: "alert" }) }),
      watch("asks", { parkedSessionIds: ["s"], last: report({ kind: "run", status: "ok" }) }),
    ];
    expect(sortWatches(list).map((w) => w.definition?.name)).toEqual(["asks", "alert", "warn", "ok", "new", "paused-alert"]);
  });

  it("polls quickly only while a run is active", () => {
    expect(pollInterval(undefined)).toBe(30_000);
    expect(pollInterval([watch("a")])).toBe(30_000);
    expect(pollInterval([watch("a"), watch("b", { running: true })])).toBe(5_000);
  });
});

describe("editor draft", () => {
  it("round-trips a definition through the draft", () => {
    const w = watch("prod-ingest", {
      definition: {
        name: "prod-ingest",
        brief: "Check lag",
        intervalSecs: 7200n,
        model: "claude",
        probe: "curl -fsS x",
        probeAlways: true,
        expires: "2026-11-20T18:30:00Z",
        allowedActions: ["restart worker", "clear cache"],
      },
    });
    const draft = draftFromWatch(w);
    expect(draft).toMatchObject({ every: "2", unit: "hours", allowedActions: "restart worker\nclear cache" });
    expect(buildDefinition(draft)).toEqual({
      name: "prod-ingest",
      brief: "Check lag",
      intervalSecs: 7200n,
      model: "claude",
      probe: "curl -fsS x",
      probeAlways: true,
      expires: "2026-11-20T18:30:00Z",
      allowedActions: ["restart worker", "clear cache"],
    });
  });

  it("picks the largest exact unit and rounds odd intervals to minutes", () => {
    expect(draftFromWatch(watch("a", { definition: { name: "a", intervalSecs: 172800n } }))).toMatchObject({ every: "2", unit: "days" });
    expect(draftFromWatch(watch("a", { definition: { name: "a", intervalSecs: 5400n } }))).toMatchObject({ every: "90", unit: "minutes" });
    expect(draftFromWatch(watch("a", { definition: { name: "a", intervalSecs: 330n } }))).toMatchObject({ every: "6", unit: "minutes" });
  });

  it("converts local datetime inputs to and from RFC 3339", () => {
    const local = toLocalInput("2026-11-20T18:30:00Z");
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(fromLocalInput(local)).toBe("2026-11-20T18:30:00Z");
    expect(fromLocalInput("")).toBe("");
    expect(toLocalInput("")).toBe("");
  });

  it("validates like the daemon", () => {
    const ok = { ...emptyDraft(), name: "disk", brief: "check disk" };
    expect(draftError(ok, true, NOW)).toBeNull();
    expect(draftError({ ...ok, name: "Disk" }, true, NOW)).toMatch(/Name/);
    expect(draftError({ ...ok, name: "-disk" }, true, NOW)).toMatch(/Name/);
    expect(draftError({ ...ok, brief: "  " }, true, NOW)).toMatch(/Describe/);
    expect(draftError({ ...ok, every: "4" }, true, NOW)).toMatch(/5 minutes/);
    expect(draftError({ ...ok, every: "" }, true, NOW)).toMatch(/5 minutes/);
    expect(draftError({ ...ok, every: "5" }, true, NOW)).toBeNull();
    const past = toLocalInput("2026-11-01T00:00:00Z");
    expect(draftError({ ...ok, expires: past }, true, NOW)).toMatch(/past/);
    // Editing an expired watch may keep its old expiry (the daemon allows it).
    expect(draftError({ ...ok, expires: past }, false, NOW)).toBeNull();
  });

  it("drops blank action lines and probe_always without a probe", () => {
    const def = buildDefinition({ ...emptyDraft(), name: "a", brief: "b", probeAlways: true, allowedActions: "\n one \n\n" });
    expect(def.probeAlways).toBe(false);
    expect(def.allowedActions).toEqual(["one"]);
    expect(def.intervalSecs).toBe(1800n);
    expect(def.expires).toBe("");
  });
});

describe("routes", () => {
  it("builds watch paths and names their views", () => {
    expect(paths.watches("my proj")).toBe("/p/my%20proj/watches");
    expect(paths.watches("p", "disk")).toBe("/p/p/watches/disk");
    expect(paths.watches(null)).toBe("/watches");
    expect(sectionPath("watches", null)).toBe("/watches");
    expect(routeView("/p/p/watches")).toBe("watches");
    expect(routeView("/p/p/watches/disk")).toBe("watch");
    expect(routeView("/watches/disk/x")).toBe("not_found");
  });
});

describe("timeline", () => {
  it("notes what produced a report only where the badge doesn't already say", () => {
    expect([kindNote("run"), kindNote("probe"), kindNote("skipped"), kindNote("failed")]).toEqual(["model run", "probe only", "", ""]);
  });
});
