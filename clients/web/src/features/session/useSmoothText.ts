import { useLayoutEffect, useRef, useState } from "react";
import { advanceSmoothText, createSmoothText, setSmoothTarget, type SmoothTextState } from "./smoothText";

export function useSmoothText(text: string): string {
  const state = useRef<SmoothTextState | null>(null);
  if (state.current === null) state.current = setSmoothTarget(createSmoothText(), text, performance.now());
  const [shown, setShown] = useState(text);

  useLayoutEffect(() => {
    let previous = performance.now();
    const oldShown = state.current!.shown;
    state.current = setSmoothTarget(state.current!, text, previous);
    if (state.current.shown !== oldShown) setShown(state.current.shown);
    let frame = 0;
    const tick = (now: number) => {
      const old = state.current!;
      state.current = advanceSmoothText(old, now - previous);
      previous = now;
      if (state.current.shown !== old.shown) setShown(state.current.shown);
      if (state.current.shown !== state.current.target) frame = requestAnimationFrame(tick);
    };
    if (state.current.shown !== state.current.target) frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [text]);

  return shown;
}
