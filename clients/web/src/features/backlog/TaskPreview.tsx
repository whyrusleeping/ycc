import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import { createPortal } from "react-dom";
import { useTask } from "../../api/queries";
import { StatusPill } from "./parts";

/** Fetch only after the pointer settles; tabbing to a reference opens it immediately. */
export function useTaskPreview(project: string, id: string) {
  const tooltipId = useId();
  const [anchor, setAnchor] = useState<HTMLElement | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const cancel = useCallback(() => clearTimeout(timer.current), []);
  useEffect(() => cancel, [cancel]);
  const close = useCallback(() => {
    cancel();
    setAnchor(null);
  }, [cancel]);
  const leave = () => {
    cancel();
    // Let the pointer cross the gap from the number to the tooltip.
    timer.current = setTimeout(() => setAnchor(null), 120);
  };
  const trigger = {
    "aria-describedby": anchor ? tooltipId : undefined,
    ...(anchor && { title: "" }), // Don't cover the preview with a native tooltip.
    onMouseEnter: (e: MouseEvent<HTMLElement>) => {
      cancel();
      const target = e.currentTarget;
      timer.current = setTimeout(() => setAnchor(target), 240);
    },
    onMouseLeave: leave,
    onFocus: (e: { currentTarget: HTMLElement }) => { cancel(); setAnchor(e.currentTarget); },
    onBlur: close,
  };
  const preview = anchor && createPortal(
    <TaskPreview project={project} id={id} tooltipId={tooltipId} anchor={anchor} close={close} onEnter={cancel} onLeave={leave} />,
    anchor.closest("dialog[open]") ?? document.body,
  );
  return { trigger, preview };
}

function TaskPreview({ project, id, tooltipId, anchor, close, onEnter, onLeave }: {
  project: string; id: string; tooltipId: string; anchor: HTMLElement; close: () => void; onEnter: () => void; onLeave: () => void;
}) {
  const task = useTask(project, id);
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current!;
    const rect = anchor.getBoundingClientRect();
    const gap = 6;
    const above = rect.top - el.offsetHeight - gap;
    el.style.top = `${rect.bottom + gap + el.offsetHeight > window.innerHeight && above >= gap ? above : rect.bottom + gap}px`;
    el.style.left = `${Math.max(gap, Math.min(rect.left, window.innerWidth - el.offsetWidth - gap))}px`;
  }, [anchor, task.data, task.isError]);
  useEffect(() => {
    const escape = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.stopPropagation(); close(); }
    };
    window.addEventListener("keydown", escape, true);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("keydown", escape, true);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [close]);
  return (
    <div
      ref={ref} role="tooltip" id={tooltipId} className="task-preview"
      onMouseEnter={onEnter} onMouseLeave={onLeave} onClick={(e) => e.stopPropagation()}
    >
      {task.data ? (
        <><StatusPill status={task.data.status} /><span>{task.data.title}</span></>
      ) : task.isError ? `Task #${id} unavailable` : `Loading task #${id}…`}
    </div>
  );
}
