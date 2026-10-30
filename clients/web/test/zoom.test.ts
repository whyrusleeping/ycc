import { describe, expect, it } from "vitest";
import { clampView, clickZoomScale, fitView, isZoomed, MAX_ZOOM, panBy, wheelFactor, zoomTo } from "../src/ui/zoom";

const stage = { w: 1000, h: 800 };
const big = { w: 2000, h: 1000 }; // fits at 0.5
const small = { w: 200, h: 100 }; // fits at 1 (never enlarged)

describe("lightbox zoom geometry", () => {
  it("fits large images and centres them; small ones stay actual size", () => {
    expect(fitView(big, stage)).toEqual({ s: 0.5, x: 0, y: 150 });
    expect(fitView(small, stage)).toEqual({ s: 1, x: 400, y: 350 });
  });

  it("keeps the image pixel under the pointer fixed while zooming", () => {
    const v = fitView(big, stage);
    const z = zoomTo(v, 1, 250, 400, big, stage);
    // Stage (250, 400) was image pixel (500, 500); it still is.
    expect(z.s).toBe(1);
    expect((250 - z.x) / z.s).toBeCloseTo(500);
    expect((400 - z.y) / z.s).toBeCloseTo(500);
  });

  it("clamps scale between fit and the maximum", () => {
    const v = fitView(big, stage);
    expect(zoomTo(v, 0.1, 0, 0, big, stage).s).toBe(0.5);
    expect(zoomTo(v, 100, 0, 0, big, stage).s).toBe(MAX_ZOOM);
  });

  it("can't pan past the image edges, and centres a dimension smaller than the stage", () => {
    const z = clampView({ s: 1, x: 0, y: 0 }, big, stage); // 2000×1000 in 1000×800
    expect(panBy(z, 500, 500, big, stage)).toEqual({ s: 1, x: 0, y: 0 });
    expect(panBy(z, -5000, -5000, big, stage)).toEqual({ s: 1, x: -1000, y: -200 });
    const wide = { w: 2000, h: 200 };
    expect(clampView({ s: 1, x: -10, y: -50 }, wide, stage)).toEqual({ s: 1, x: -10, y: 300 });
  });

  it("toggles to actual size, or to double the fitted size when actual is close", () => {
    expect(clickZoomScale({ w: 4000, h: 2000 }, stage)).toBe(1);
    expect(clickZoomScale(big, stage)).toBe(1);
    expect(clickZoomScale(small, stage)).toBe(2);
    expect(isZoomed(fitView(big, stage), big, stage)).toBe(false);
    expect(isZoomed(zoomTo(fitView(big, stage), 0.6, 0, 0, big, stage), big, stage)).toBe(true);
  });

  it("zooms in for wheel up and out for wheel down, pinches more sensitively", () => {
    expect(wheelFactor(-100, 0, false)).toBeGreaterThan(1);
    expect(wheelFactor(100, 0, false)).toBeLessThan(1);
    expect(wheelFactor(-3, 1, false)).toBeCloseTo(wheelFactor(-48, 0, false));
    expect(wheelFactor(-10, 0, true)).toBeGreaterThan(wheelFactor(-10, 0, false));
  });
});
