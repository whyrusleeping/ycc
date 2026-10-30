// Pan/zoom geometry for the image lightbox. A view places the image's
// top-left corner at (x, y) in stage pixels, drawn at scale s. Views never
// zoom out past fitting the stage, and a dimension smaller than the stage is
// centred; a larger one can't be panned past its edges.

export interface View {
  s: number;
  x: number;
  y: number;
}

export interface Box {
  w: number;
  h: number;
}

export const MAX_ZOOM = 8;

/** The scale that fits the image in the stage, never enlarging it. */
export function fitScale(img: Box, stage: Box): number {
  if (!img.w || !img.h || !stage.w || !stage.h) return 1;
  return Math.min(1, stage.w / img.w, stage.h / img.h);
}

function clampScale(s: number, img: Box, stage: Box): number {
  const min = fitScale(img, stage);
  return Math.min(Math.max(s, min), Math.max(MAX_ZOOM, min));
}

function clampAxis(pos: number, size: number, room: number): number {
  return size <= room ? (room - size) / 2 : Math.min(0, Math.max(room - size, pos));
}

export function clampView(v: View, img: Box, stage: Box): View {
  const s = clampScale(v.s, img, stage);
  return { s, x: clampAxis(v.x, img.w * s, stage.w), y: clampAxis(v.y, img.h * s, stage.h) };
}

export function fitView(img: Box, stage: Box): View {
  return clampView({ s: 0, x: 0, y: 0 }, img, stage);
}

/** Scale to `s`, keeping the image pixel under stage point (px, py) in place. */
export function zoomTo(v: View, s: number, px: number, py: number, img: Box, stage: Box): View {
  const next = clampScale(s, img, stage);
  const k = next / v.s;
  return clampView({ s: next, x: px - (px - v.x) * k, y: py - (py - v.y) * k }, img, stage);
}

export function panBy(v: View, dx: number, dy: number, img: Box, stage: Box): View {
  return clampView({ s: v.s, x: v.x + dx, y: v.y + dy }, img, stage);
}

export function isZoomed(v: View, img: Box, stage: Box): boolean {
  return v.s > fitScale(img, stage) * 1.001;
}

/**
 * The zoom factor for a wheel event. Trackpad pinches arrive as ctrl+wheel
 * with small deltas; mouse wheels as larger steps (or lines/pages).
 */
export function wheelFactor(deltaY: number, deltaMode: number, pinch: boolean): number {
  const px = deltaMode === 1 ? deltaY * 16 : deltaMode === 2 ? deltaY * 400 : deltaY;
  return Math.exp(-px * (pinch ? 0.01 : 0.0015));
}

/**
 * Where a click on the fitted image zooms to: actual size when that's at
 * least double, otherwise double the fitted size.
 */
export function clickZoomScale(img: Box, stage: Box): number {
  const fit = fitScale(img, stage);
  return fit < 0.5 ? 1 : 2 * fit;
}
