// The inspector: a closable, resizable right-hand pane for contextual detail
// (expanded transcript rows, working-tree and commit diffs) so detail opens
// beside the transcript instead of replacing it. Later phases add task detail
// and file contents as further InspectorItem kinds.
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";

export type InspectorItem =
  | { kind: "row"; project: string; sessionId: string; rowId: string }
  | {
      kind: "workingChanges";
      project: string;
      sessionId: string;
      taskId?: string;
      knownSnapshotId?: string;
    }
  | { kind: "commit"; project: string; sha: string };

interface InspectorState {
  item: InspectorItem | null;
  open: (item: InspectorItem) => void;
  close: () => void;
  width: number;
  setWidth: (w: number) => void;
}

const WIDTH_KEY = "ycc.inspectorWidth";
const MIN_WIDTH = 280;

const Ctx = createContext<InspectorState | null>(null);

function initialWidth(): number {
  try {
    const v = Number(localStorage.getItem(WIDTH_KEY));
    if (Number.isFinite(v) && v >= MIN_WIDTH) return v;
  } catch {
    // ignore
  }
  return 440;
}

export function InspectorProvider({ children }: { children: ReactNode }) {
  const [item, setItem] = useState<InspectorItem | null>(null);
  const [width, setWidthState] = useState(initialWidth);
  const setWidth = useCallback((w: number) => {
    const clamped = Math.max(MIN_WIDTH, Math.min(w, Math.round(window.innerWidth * 0.7)));
    setWidthState(clamped);
    try {
      localStorage.setItem(WIDTH_KEY, String(clamped));
    } catch {
      // ignore
    }
  }, []);
  const value = useMemo(
    () => ({ item, open: setItem, close: () => setItem(null), width, setWidth }),
    [item, width, setWidth],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useInspector(): InspectorState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useInspector outside InspectorProvider");
  return v;
}

/** Drag handle on the inspector's left edge. */
export function InspectorResizer() {
  const { width, setWidth } = useInspector();
  const start = useRef<{ x: number; w: number } | null>(null);
  return (
    <div
      className="inspector-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize inspector"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft") setWidth(width + 24);
        if (e.key === "ArrowRight") setWidth(width - 24);
      }}
      onPointerDown={(e) => {
        start.current = { x: e.clientX, w: width };
        e.currentTarget.setPointerCapture(e.pointerId);
      }}
      onPointerMove={(e) => {
        if (start.current) setWidth(start.current.w + (start.current.x - e.clientX));
      }}
      onPointerUp={() => {
        start.current = null;
      }}
    />
  );
}
