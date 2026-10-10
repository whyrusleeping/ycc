import { describe, expect, it } from "vitest";
import { inspectorLayoutWidth } from "../src/features/inspector/inspector";

describe("inspector layout budget", () => {
  it("keeps a usable conversation at 1024px, even with a large saved inspector", () => {
    const available = 1024 - 260;
    for (const preference of [280, 440, 900]) {
      const width = inspectorLayoutWidth(preference, available);
      expect(width).toBeGreaterThanOrEqual(280);
      expect(available - width).toBeGreaterThanOrEqual(480);
    }
  });

  it("respects desktop resizing and restores the preference after temporary narrowing", () => {
    const preferred = 640;
    expect(inspectorLayoutWidth(preferred, 1024 - 260)).toBe(284);
    expect(inspectorLayoutWidth(preferred, 1440 - 292)).toBe(preferred);
    expect(inspectorLayoutWidth(900, 1440 - 292)).toBe(668);
    expect(inspectorLayoutWidth(200, 1440 - 292)).toBe(280);
  });

  it("retains a readable inspector when the viewport cannot fit both minimums", () => {
    expect(inspectorLayoutWidth(440, 600)).toBe(280);
  });
});
