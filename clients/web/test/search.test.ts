// Transcript search: matching over each row's full text, stepping older/newer
// with wrap-around, and paging earlier history in to find a match that is not
// loaded yet — driven through a real SessionController against a fake daemon.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it, vi } from "vitest";
import {
  EventSchema,
  GetSessionViewPageResponseSchema,
  GetSessionViewResponseSchema,
  ListSessionsResponseSchema,
  SessionPresentationRowSchema,
  SessionViewStateSchema,
} from "../src/gen/ycc/v1/ycc_pb";
import { SessionController, type SessionApi } from "../src/features/session/controller";
import type { TranscriptRow } from "../src/features/session/projection";
import { findMatch, matchingRowIds, rowSearchText, type SearchSource } from "../src/features/session/search";

function row(id: string, text: string): TranscriptRow {
  return { id, kind: { type: "model", text }, seq: 0, updatedSeq: 0, actor: "coordinator", ts: "", detailAvailable: false };
}

/** A fake paged history: pages[0] is loaded, later entries are earlier pages. */
function pagedSource(pages: TranscriptRow[][]): SearchSource & { loads: number } {
  let loaded = 1;
  const src = {
    loads: 0,
    rows: () => pages.slice(0, loaded).reverse().flat(),
    hasEarlier: () => loaded < pages.length,
    loadEarlier: async () => {
      if (loaded >= pages.length) return false;
      loaded++;
      src.loads++;
      return true;
    },
  };
  return src;
}

describe("search matching", () => {
  it("searches every textual field, case-insensitively", () => {
    const tool: TranscriptRow = {
      ...row("t", ""),
      kind: { type: "tool", name: "Bash", status: "ok", args: '{"command":"go test ./..."}', output: "PASS ok" },
    };
    expect(rowSearchText(tool)).toContain("go test");
    expect(matchingRowIds([row("a", "Hello World"), tool, row("b", "nothing")], "  WORLD ")).toEqual(["a"]);
    expect(matchingRowIds([tool], "pass")).toEqual(["t"]);
    expect(matchingRowIds([tool], "")).toEqual([]);
  });

  it("starts at the newest match and steps older, then wraps", async () => {
    const src = pagedSource([[row("1", "x hit"), row("2", "miss"), row("3", "hit y")]]);
    expect(await findMatch(src, "hit", null, "older")).toEqual({ kind: "found", rowId: "3", wrapped: false });
    expect(await findMatch(src, "hit", "3", "older")).toEqual({ kind: "found", rowId: "1", wrapped: false });
    expect(await findMatch(src, "hit", "1", "older")).toEqual({ kind: "found", rowId: "3", wrapped: true });
    expect(await findMatch(src, "hit", "1", "newer")).toEqual({ kind: "found", rowId: "3", wrapped: false });
    expect(await findMatch(src, "hit", "3", "newer")).toEqual({ kind: "found", rowId: "1", wrapped: true });
    expect(await findMatch(src, "zzz", null, "older")).toEqual({ kind: "none" });
  });

  it("pages earlier history until an older match appears", async () => {
    const src = pagedSource([
      [row("n1", "recent"), row("n2", "recent hit")],
      [row("m1", "middle"), row("m2", "middle")],
      [row("o1", "old hit"), row("o2", "old")],
    ]);
    expect(await findMatch(src, "hit", "n2", "older")).toEqual({ kind: "found", rowId: "o1", wrapped: false });
    expect(src.loads).toBe(2);
    // Newer past the newest match stays put while history is incomplete…
    const partial = pagedSource([[row("a", "hit")], [row("b", "hit")]]);
    expect(await findMatch(partial, "hit", "a", "newer")).toEqual({ kind: "end", rowId: "a" });
    expect(partial.loads).toBe(0);
  });

  it("stops paging when cancelled", async () => {
    const src = pagedSource([[row("a", "x")], [row("b", "x")], [row("c", "hit")]]);
    const result = await findMatch(src, "hit", null, "older", () => src.loads >= 1);
    expect(result).toEqual({ kind: "cancelled" });
    expect(src.loads).toBe(1);
  });
});

// MARK: through a SessionController

function modelRow(seq: number, text: string) {
  return create(SessionPresentationRowSchema, {
    id: `seq-${seq}`,
    positionSeq: BigInt(seq),
    updatedSeq: BigInt(seq),
    events: [create(EventSchema, { seq: BigInt(seq), actor: "coordinator", type: "model_turn", dataJson: JSON.stringify({ text }) })],
  });
}

describe("search through the controller", () => {
  it("finds a match in an earlier, not-yet-loaded page", async () => {
    const pages: Record<string, { rows: number[]; next: string }> = {
      c1: { rows: [20, 21, 22], next: "c2" },
      c2: { rows: [10, 11, 12], next: "" },
    };
    const text = (seq: number) => (seq === 11 ? "the needle is here" : `filler ${seq}`);
    const getSessionViewPage = vi.fn(async (req: { cursor: string }) => {
      const p = pages[req.cursor];
      return create(GetSessionViewPageResponseSchema, {
        rows: p.rows.map((s) => modelRow(s, text(s))),
        indexedThroughSeq: 32n,
        earlierCursor: p.next,
      });
    });
    const api = {
      getSessionView: vi.fn(async () =>
        create(GetSessionViewResponseSchema, {
          state: create(SessionViewStateSchema, { indexedThroughSeq: 32n, phase: "idle", rolloverAvailable: true }),
          rows: [30, 31, 32].map((s) => modelRow(s, text(s))),
          earlierCursor: "c1",
        }),
      ),
      listSessions: vi.fn(async () => create(ListSessionsResponseSchema, { sessions: [] })),
      getSessionViewPage,
      subscribeSessionView: vi.fn(),
      getSessionViewDetail: vi.fn(),
    } as unknown as SessionApi;
    const c = new SessionController(api, "p", "s1", { onUnauthorized: vi.fn(), onError: vi.fn() }, { publishDelayMs: 0 });
    c.start();
    for (let i = 0; i < 200 && c.getSnapshot().conn !== "finished"; i++) await new Promise((r) => setTimeout(r, 2));
    expect(c.getSnapshot().rows.map((r) => r.id)).toEqual(["seq-30", "seq-31", "seq-32"]);

    const source: SearchSource = { rows: () => c.projection.rows, hasEarlier: () => c.hasEarlier(), loadEarlier: () => c.loadEarlier() };
    const result = await findMatch(source, "NEEDLE", null, "older");
    expect(result).toEqual({ kind: "found", rowId: "seq-11", wrapped: false });
    expect(getSessionViewPage).toHaveBeenCalledTimes(2);
    expect(c.getSnapshot().rows.map((r) => r.id)).toContain("seq-11");
    expect(c.getSnapshot().hasEarlier).toBe(false);
    c.dispose();
  });

  it("shares an in-flight earlier-page load", async () => {
    let resolve!: () => void;
    const gate = new Promise<void>((r) => (resolve = r));
    const getSessionViewPage = vi.fn(async () => {
      await gate;
      return create(GetSessionViewPageResponseSchema, { rows: [modelRow(1, "a")], indexedThroughSeq: 5n, earlierCursor: "" });
    });
    const api = {
      getSessionView: vi.fn(async () =>
        create(GetSessionViewResponseSchema, {
          state: create(SessionViewStateSchema, { indexedThroughSeq: 5n, phase: "idle" }),
          rows: [modelRow(5, "b")],
          earlierCursor: "c1",
        }),
      ),
      listSessions: vi.fn(async () => create(ListSessionsResponseSchema, { sessions: [] })),
      getSessionViewPage,
      subscribeSessionView: vi.fn(),
      getSessionViewDetail: vi.fn(),
    } as unknown as SessionApi;
    const c = new SessionController(api, "p", "s1", { onUnauthorized: vi.fn(), onError: vi.fn() }, { publishDelayMs: 0 });
    c.start();
    for (let i = 0; i < 200 && !c.getSnapshot().installed; i++) await new Promise((r) => setTimeout(r, 2));
    const first = c.loadEarlier();
    const second = c.loadEarlier();
    resolve();
    expect(await first).toBe(true);
    expect(await second).toBe(true);
    expect(getSessionViewPage).toHaveBeenCalledTimes(1);
    expect(await c.loadEarlier()).toBe(false);
    c.dispose();
  });
});
