// The inspector: a closable, resizable right-hand pane for contextual detail
// (expanded transcript rows, working-tree and commit diffs) so detail opens
// beside the transcript instead of replacing it: also full-size transcript
// pictures, the per-session settings panel, and backlog task detail (opened
// from a session's focus-task chips), a workstream's merge preview, and file
// contents (opened from file references). Opening something from inside the
// inspector (`push`) keeps a back stack; opening from elsewhere replaces it.
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from "react";
import { track } from "../../app/analytics";
import type { LineRange } from "../files/fileReference";

export type InspectorItem =
  | { kind: "row"; project: string; sessionId: string; rowId: string }
  | {
      kind: "workingChanges";
      project: string;
      sessionId: string;
      taskId?: string;
      /** The snapshot a review verdict covered (opened from a review row). */
      knownSnapshotId?: string;
      verdict?: string;
      reviewHeading?: string;
    }
  | { kind: "commit"; project: string; sha: string }
  | {
      kind: "picture";
      project: string;
      sessionId: string;
      attachmentId: string;
      filename: string;
      mediaType: string;
    }
  | { kind: "sessionSettings"; project: string; sessionId: string }
  | { kind: "task"; project: string; taskId: string }
  /** A workstream's merge preview and accept gate. */
  | { kind: "merge"; project: string; workstreamId: string; branch: string }
  /** A project file (or directory), against a session's worktree when `sessionId` is set. */
  | { kind: "file"; project: string; sessionId: string; path: string; isDirectory: boolean; lines: LineRange | null };

interface InspectorState {
  item: InspectorItem | null;
  /** Show `item`, replacing whatever was open (and its back stack). */
  open: (item: InspectorItem) => void;
  /** Show `item` from inside the inspector: the current item becomes "Back". */
  push: (item: InspectorItem) => void;
  /** Return to the item a `push` replaced; false when there is none. */
  back: () => boolean;
  canGoBack: boolean;
  close: () => void;
  /** Close, or re-open what was last closed; false when there is nothing to show. */
  toggle: () => boolean;
  width: number;
  maxWidth: number;
  setWidth: (w: number) => void;
  /** Shell width remaining after navigation; updated on layout changes. */
  setAvailableWidth: (w: number) => void;
}

const WIDTH_KEY = "ycc.inspectorWidth";
const MIN_WIDTH = 280;
const MIN_MAIN_WIDTH = 480;

/** Reserve room for the conversation, not a percentage of the entire viewport. */
export function inspectorLayoutWidth(preferred: number, available: number): number {
  return Math.max(MIN_WIDTH, Math.min(preferred, available - MIN_MAIN_WIDTH));
}

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
  const [stack, setStack] = useState<InspectorItem[]>([]);
  const item = stack.length ? stack[stack.length - 1] : null;
  // Which kinds of detail get opened, and from where (the view field), never what.
  const open = useCallback((next: InspectorItem) => {
    track.action("inspector.open", "", { kind: next.kind });
    setStack([next]);
  }, []);
  const push = useCallback((next: InspectorItem) => {
    track.action("inspector.open", "", { kind: next.kind, stacked: true });
    setStack((s) => [...s.slice(-19), next]);
  }, []);
  const backRef = useRef(stack);
  backRef.current = stack;
  const back = useCallback(() => {
    if (backRef.current.length < 2) return false;
    setStack((s) => s.slice(0, -1));
    return true;
  }, []);
  const closed = useRef<InspectorItem[]>([]);
  const close = useCallback(() => {
    if (backRef.current.length) closed.current = backRef.current;
    setStack([]);
  }, []);
  const toggle = useCallback(() => {
    if (backRef.current.length) {
      close();
      return true;
    }
    if (!closed.current.length) return false;
    setStack(closed.current);
    return true;
  }, [close]);
  const [preferredWidth, setWidthState] = useState(initialWidth);
  const [availableWidth, setAvailableWidth] = useState(Infinity);
  const width = inspectorLayoutWidth(preferredWidth, availableWidth);
  const maxWidth = inspectorLayoutWidth(Infinity, availableWidth);
  // Automatic narrowing must not overwrite the user's desktop preference.
  const setWidth = useCallback((w: number) => {
    const clamped = inspectorLayoutWidth(w, availableWidth);
    setWidthState(clamped);
    try {
      localStorage.setItem(WIDTH_KEY, String(clamped));
    } catch {
      // ignore
    }
  }, [availableWidth]);
  const value = useMemo(
    () => ({ item, open, push, back, canGoBack: stack.length > 1, close, toggle, width, maxWidth, setWidth, setAvailableWidth }),
    [item, open, push, back, stack.length, close, toggle, width, maxWidth, setWidth],
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
  const { width, maxWidth, setWidth } = useInspector();
  const start = useRef<{ x: number; w: number } | null>(null);
  return (
    <div
      className="inspector-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize inspector"
      aria-valuemin={MIN_WIDTH}
      aria-valuemax={Number.isFinite(maxWidth) ? maxWidth : undefined}
      aria-valuenow={width}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
          e.preventDefault();
          setWidth(width + (e.key === "ArrowLeft" ? 24 : -24));
        }
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
      onPointerCancel={() => {
        start.current = null;
      }}
    />
  );
}
