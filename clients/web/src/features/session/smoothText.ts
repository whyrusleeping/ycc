// Pace prefix-growing snapshots over the observed arrival interval. Times are
// milliseconds; rate is UTF-16 code units/second, with pair-safe reveal edges.
export interface SmoothTextState {
  shown: string;
  target: string;
  rate: number;
  remainder: number;
  intervalMs: number;
  arrivedAt: number | null;
}

const MAX_BACKLOG = 2000;
const MIN_RATE = 40;

export function createSmoothText(): SmoothTextState {
  return { shown: "", target: "", rate: 0, remainder: 0, intervalMs: 100, arrivedAt: null };
}

function splitsPair(text: string, end: number): boolean {
  const before = text.charCodeAt(end - 1);
  const after = text.charCodeAt(end);
  return before >= 0xd800 && before <= 0xdbff && after >= 0xdc00 && after <= 0xdfff;
}

export function setSmoothTarget(state: SmoothTextState, target: string, now: number): SmoothTextState {
  if (state.arrivedAt !== null && target === state.target) return state;
  if (state.arrivedAt === null || !target.startsWith(state.target)) {
    return { ...createSmoothText(), shown: target, target, arrivedAt: now };
  }
  const intervalMs = state.intervalMs * 0.7 + Math.max(0, now - state.arrivedAt) * 0.3;
  let end = Math.max(state.shown.length, target.length - MAX_BACKLOG);
  // Snap large bursts forward without leaving half an emoji on screen.
  if (splitsPair(target, end)) end++;
  const shown = target.slice(0, end);
  const duration = Math.min(250, Math.max(50, intervalMs));
  return {
    shown,
    target,
    rate: Math.max(MIN_RATE, ((target.length - end) * 1000) / duration),
    remainder: end === state.shown.length ? state.remainder : 0,
    intervalMs,
    arrivedAt: now,
  };
}

export function advanceSmoothText(state: SmoothTextState, dt: number): SmoothTextState {
  if (state.shown === state.target || dt <= 0) return state;
  const budget = state.remainder + (state.rate * dt) / 1000;
  let end = Math.min(state.target.length, state.shown.length + Math.floor(budget));
  if (splitsPair(state.target, end)) end--;
  return {
    ...state,
    shown: state.target.slice(0, end),
    remainder: end === state.target.length ? 0 : budget - (end - state.shown.length),
  };
}
