// Session coordination helpers: held-task holder labels, session-id linking in notice
// text, coordination_notice rows, and notice expiry labels.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ProjectNoticeSchema, TaskHolderSchema } from "../src/gen/ycc/v1/ycc_pb";
import {
  activityBadge,
  compactHolderLabel,
  coordinationHeadline,
  coordinationVariant,
  expiresIn,
  holderLabel,
  posterLabel,
  splitSessionIds,
} from "../src/features/coordination/model";
import { SessionProjection } from "../src/features/session/projection";
import { rowSearchText } from "../src/features/session/search";

const A = "s_0123456789abcdef";
const B = "s_fedcba9876543210";
const NAMED = "s_amber-cedar-4f2a";

describe("holderLabel", () => {
  it("names a holder: loop sessions by short id, others by title", () => {
    expect(holderLabel(create(TaskHolderSchema, { sessionId: A, loop: true, title: "Work on the backlog" }))).toBe("loop s_01234567…");
    expect(holderLabel(create(TaskHolderSchema, { sessionId: NAMED, loop: true, title: "Work on the backlog" }))).toBe("loop amber-cedar");
    expect(holderLabel(create(TaskHolderSchema, { sessionId: A, title: "Fix the login flow" }))).toBe("Fix the login flow");
    expect(holderLabel(create(TaskHolderSchema, { sessionId: A }))).toBe("s_01234567…");
    expect(holderLabel(create(TaskHolderSchema, { sessionId: NAMED }))).toBe("amber-cedar");
    expect(compactHolderLabel(create(TaskHolderSchema, { sessionId: NAMED, loop: true }))).toBe("amber-cedar");
    expect(compactHolderLabel(create(TaskHolderSchema, { sessionId: A, loop: true }))).toBe("loop");
    expect(compactHolderLabel(create(TaskHolderSchema, { sessionId: A, title: "Fix the login flow" }))).toBe("Fix the login flow");
  });
});

describe("splitSessionIds", () => {
  it("splits named and legacy session ids out of text", () => {
    expect(splitSessionIds(`[coordination notice] Session ${A} just wrote a.go; ${NAMED} and ${B} too.`)).toEqual([
      { kind: "text", text: "[coordination notice] Session " },
      { kind: "session", id: A },
      { kind: "text", text: " just wrote a.go; " },
      { kind: "session", id: NAMED },
      { kind: "text", text: " and " },
      { kind: "session", id: B },
      { kind: "text", text: " too." },
    ]);
  });

  it("leaves text without ids (or with lookalikes) whole", () => {
    expect(splitSessionIds("no ids here, s_123 or xs_0123456789abcdefg")).toEqual([
      { kind: "text", text: "no ids here, s_123 or xs_0123456789abcdefg" },
    ]);
    expect(splitSessionIds("")).toEqual([]);
  });
});

describe("coordination rows", () => {
  const digest = `[project notice board] The current notice board and other sessions' intents in this project.\nActive notices:\n- n2 "deploying" posted by ${A}\n`;
  const overlap = `[coordination notice] Session ${A} just wrote internal/x.go, which holds your uncommitted work.`;

  it("tells a board digest from an overlap notice and strips the tag from the headline", () => {
    expect(coordinationVariant(digest)).toBe("board");
    expect(coordinationVariant(overlap)).toBe("overlap");
    expect(coordinationHeadline(digest)).toBe("The current notice board and other sessions' intents in this project.");
    expect(coordinationHeadline(overlap)).toBe(`Session ${A} just wrote internal/x.go, which holds your uncommitted work.`);
  });

  it("folds coordination_notice events into a coordination row with the full text", () => {
    const p = new SessionProjection();
    p.apply({ seq: 1, ts: "", actor: "system", type: "coordination_notice", dataJson: JSON.stringify({ text: digest }), transient: false });
    p.apply({ seq: 2, ts: "", actor: "system", type: "coordination_notice", dataJson: JSON.stringify({ text: "" }), transient: false });
    expect(p.durableRows).toHaveLength(1);
    expect(p.durableRows[0].kind).toEqual({ type: "coordination", text: digest.trim() });
    expect(rowSearchText(p.durableRows[0])).toContain("deploying");
    expect(p.phase.kind).toBe("running");
  });
});

describe("notice labels", () => {
  const now = Date.parse("2026-11-22T20:00:00Z");
  it("formats expiry", () => {
    expect(expiresIn("2026-11-22T20:07:00Z", now)).toBe("in 7m");
    expect(expiresIn("2026-11-22T21:12:00Z", now)).toBe("in 1h12m");
    expect(expiresIn("2026-11-22T23:00:00Z", now)).toBe("in 3h");
    expect(expiresIn("2026-11-22T19:59:00Z", now)).toBe("expired");
    expect(expiresIn("", now)).toBe("");
  });

  it("names the poster and the activity", () => {
    expect(posterLabel(create(ProjectNoticeSchema, { sessionId: "" }))).toBe("you");
    expect(posterLabel(create(ProjectNoticeSchema, { sessionId: A, actor: "coordinator" }))).toBe("");
    expect(posterLabel(create(ProjectNoticeSchema, { sessionId: A, actor: "implementer" }))).toBe("implementer");
    expect(activityBadge("question")).toEqual({ label: "waiting on a question", tone: "waiting" });
    expect(activityBadge("weird").label).toBe("weird");
  });
});
