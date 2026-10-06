// Open/closed state of the command palette and the shortcuts help overlay,
// reachable from non-React code (actions, shortcuts).
import { useSyncExternalStore } from "react";

export interface OverlayState {
  palette: { open: boolean; seq: number; query: string };
  help: boolean;
}

let state: OverlayState = { palette: { open: false, seq: 0, query: "" }, help: false };
const listeners = new Set<() => void>();

function set(next: OverlayState) {
  state = next;
  for (const l of listeners) l();
}

export function openPalette(query = "") {
  set({ help: false, palette: { open: true, seq: state.palette.seq + 1, query } });
}

export function closePalette() {
  if (state.palette.open) set({ ...state, palette: { ...state.palette, open: false } });
}

export function togglePalette() {
  if (state.palette.open) closePalette();
  else openPalette();
}

export function openHelp() {
  set({ palette: { ...state.palette, open: false }, help: true });
}

export function closeHelp() {
  if (state.help) set({ ...state, help: false });
}

export function useOverlays(): OverlayState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
  );
}
