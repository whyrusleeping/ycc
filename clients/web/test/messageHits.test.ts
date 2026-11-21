// Session message search helpers: which project is searched, which queries are
// sent, the indexing notice, and where a hit opens.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ProjectInfoSchema, SearchSessionsResponseSchema, SessionSearchHitSchema, SessionSummarySchema } from "../src/gen/ycc/v1/ycc_pb";
import { hitPath, indexingNote, searchableQuery, searchProject } from "../src/features/sessions/messageHits";

describe("message search helpers", () => {
  const projects = [create(ProjectInfoSchema, { name: "alpha" }), create(ProjectInfoSchema, { name: "beta" })];

  it("searches the preferred registered project, else the sole one", () => {
    expect(searchProject("beta", projects)).toBe("beta");
    expect(searchProject("gone", projects)).toBeNull();
    expect(searchProject(null, projects)).toBeNull();
    expect(searchProject(null, projects.slice(0, 1))).toBe("alpha");
    expect(searchProject("alpha", [])).toBeNull();
  });

  it("sends only queries of two or more characters", () => {
    expect(searchableQuery(" a ")).toBe("");
    expect(searchableQuery(" ab ")).toBe("ab");
  });

  it("says when the index is still building", () => {
    expect(indexingNote(undefined)).toBeNull();
    expect(indexingNote(create(SearchSessionsResponseSchema, { indexing: false }))).toBeNull();
    expect(indexingNote(create(SearchSessionsResponseSchema, { indexing: true, indexedSessions: 3, totalSessions: 40 }))).toBe(
      "Still indexing session history (3 of 40 sessions); results may be incomplete.",
    );
  });

  it("opens a hit at its row", () => {
    const hit = create(SessionSearchHitSchema, { session: create(SessionSummarySchema, { sessionId: "s 1" }), rowId: "seq-7" });
    expect(hitPath("alpha", hit)).toBe("/p/alpha/s/s%201?row=seq-7");
    expect(hitPath("", hit)).toBe("/s/s%201?row=seq-7");
  });
});
