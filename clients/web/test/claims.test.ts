// Live session claims split into definite and ambiguous ones.
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { ClaimedPathSchema } from "../src/gen/ycc/v1/ycc_pb";
import { splitClaims } from "../src/features/coordination/claims";

describe("splitClaims", () => {
  it("keeps order within each group and separates ambiguous claims", () => {
    const { definite, ambiguous } = splitClaims([
      create(ClaimedPathSchema, { path: "gen.go", ambiguous: true }),
      create(ClaimedPathSchema, { path: "a.go" }),
      create(ClaimedPathSchema, { path: "gen2.go", ambiguous: true }),
      create(ClaimedPathSchema, { path: "b.go" }),
    ]);
    expect(definite.map((c) => c.path)).toEqual(["a.go", "b.go"]);
    expect(ambiguous.map((c) => c.path)).toEqual(["gen.go", "gen2.go"]);
  });
});
