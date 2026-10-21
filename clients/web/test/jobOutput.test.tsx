// @vitest-environment jsdom
// The job-output viewer falls back to the node's final result when the
// daemon stops serving output mid-follow.
import { create } from "@bufbuild/protobuf";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { JobOutput } from "../src/features/activity/JobOutput";
import { GetJobOutputResponseSchema } from "../src/gen/ycc/v1/ycc_pb";

const { getJobOutput } = vi.hoisted(() => ({ getJobOutput: vi.fn() }));
vi.mock("../src/api/client", () => ({
  client: { getJobOutput },
  errorMessage: (err: unknown) => String(err),
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.useFakeTimers();
  getJobOutput.mockReset();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("shows the final result, not a stale running view, once output becomes unavailable", async () => {
  const partial = new TextEncoder().encode("building…\n");
  getJobOutput
    .mockResolvedValueOnce(
      create(GetJobOutputResponseSchema, { available: true, status: "running", data: partial, start: 0n, end: 10n, retainedEnd: 10n }),
    )
    .mockResolvedValueOnce(create(GetJobOutputResponseSchema, { available: false, unavailableReason: "The session stopped." }));
  await act(async () => {
    root.render(<JobOutput project="p" sessionId="s" jobId="job_1" available result="exit 0: build ok" />);
  });
  expect(container.querySelector(".job-output-cursor")).not.toBeNull();

  await act(() => vi.advanceTimersByTimeAsync(1000));
  expect(getJobOutput).toHaveBeenCalledTimes(2);
  const text = container.textContent ?? "";
  expect(text).toContain("The session stopped.");
  expect(text).toContain("exit 0: build ok");
  expect(text).toContain("building…");
  expect(container.querySelector(".job-output-cursor")).toBeNull();

  // Following stopped: no further reads.
  await act(() => vi.advanceTimersByTimeAsync(5000));
  expect(getJobOutput).toHaveBeenCalledTimes(2);
});
