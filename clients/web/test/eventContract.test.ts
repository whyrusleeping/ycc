// Shared event-contract fixtures (testdata/event-contract), the same scenarios
// the TUI and YccKit harnesses consume. Facts are extracted from the web
// client's real reducer (SessionProjection.apply — also how every indexed
// presentation row is decoded, and how SubscribeSessionView transient events
// are folded); a reconnect clears only the transient tails.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { SessionProjection } from "../src/features/session/projection";
import type { WireEvent } from "../src/features/session/wire";

const dir = fileURLToPath(new URL("../../../testdata/event-contract", import.meta.url));

interface ContractEvent {
  seq: number;
  actor: string;
  type: string;
  transient?: boolean;
  ts?: string;
  data: Record<string, unknown>;
}

interface Step {
  event?: ContractEvent;
  reconnect?: Record<string, never>;
  expect?: Record<string, unknown>;
}

function facts(p: SessionProjection, applied: ContractEvent[]): Record<string, unknown> {
  const answers: unknown[] = [];
  applied.forEach((ev, i) => {
    if (ev.type !== "question_answered") return;
    const question = applied
      .slice(0, i)
      .reverse()
      .find((q) => q.actor === ev.actor && q.type === "question_asked");
    if (!question) return;
    const row = p.durableRows.find((r) => r.seq === question.seq);
    answers.push({ question_seq: question.seq, provenance: row?.kind.type === "assumption" ? "automatic" : "human" });
  });
  return {
    phase: p.phase.kind,
    awaiting_jobs: p.awaitingJobs,
    pause_requested: p.pauseRequested,
    cursor: p.lastPersistedSeq,
    pending_question: p.pendingQuestion
      ? {
          prompts: p.pendingQuestion.questions.map((q) => q.prompt),
          options: p.pendingQuestion.questions.map((q) => q.options),
        }
      : null,
    inputs: p.durableRows
      .filter((r) => r.kind.type === "user")
      .map((r) => ({
        seq: r.seq,
        text: r.kind.type === "user" ? r.kind.text : "",
        delivery: r.userInputStatus === "queued" ? "queued" : "delivered",
      })),
    answers,
    reviews: p.durableRows
      .filter((r) => r.kind.type === "review")
      .map((r) => ({ seq: r.seq, verdict: r.kind.type === "review" ? r.kind.verdict : "" })),
    tails: Object.fromEntries(p.liveTails.map((t) => [t.actor, t.kind.type === "liveTail" ? t.kind.text : ""])),
  };
}

const files = readdirSync(dir)
  .filter((f) => f.endsWith(".json"))
  .sort();

describe("event contract", () => {
  it("has fixtures", () => {
    expect(files.length).toBeGreaterThan(0);
  });

  for (const file of files) {
    it(file, () => {
      const fixture = JSON.parse(readFileSync(join(dir, file), "utf8")) as { version: number; name: string; steps: Step[] };
      expect(fixture.version).toBe(1);
      const p = new SessionProjection();
      const applied: ContractEvent[] = [];
      fixture.steps.forEach((step, index) => {
        if (step.event) {
          const ev = step.event;
          const wire: WireEvent = {
            seq: ev.seq,
            actor: ev.actor,
            type: ev.type,
            transient: ev.transient ?? false,
            ts: ev.ts ?? "",
            dataJson: JSON.stringify(ev.data ?? {}),
          };
          const before = p.lastPersistedSeq;
          p.apply(wire);
          if (!wire.transient && wire.seq > before) applied.push(ev);
        } else if (step.reconnect) {
          p.clearLiveTails();
        } else if (step.expect) {
          const actual = facts(p, applied);
          for (const [key, value] of Object.entries(step.expect)) {
            expect(Object.hasOwn(actual, key), `unknown fact ${key}`).toBe(true);
            expect(actual[key], `${fixture.name} step ${index}: ${key}`).toEqual(value);
          }
        } else {
          throw new Error(`empty step ${fixture.name} #${index}`);
        }
      });
    });
  }
});
