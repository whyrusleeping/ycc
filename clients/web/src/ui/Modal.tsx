// A modal surface built on <dialog> (focus trap, Esc, and backdrop from the
// platform). Esc calls onClose; the caller decides whether closing is allowed.
import { useEffect, useRef, type ReactNode } from "react";
import { useFlow, useTrackedView } from "../app/analytics";

export function Modal({
  open,
  onClose,
  title,
  className = "",
  view,
  flow,
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  className?: string;
  /** Analytics view name while open (docs/design/usage-analytics.md). */
  view?: string;
  /** Analytics flow: records `<flow>.open`, then `.cancel` unless the form calls track.submit(flow). */
  flow?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useTrackedView(view, open);
  useFlow(flow, open);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      className={`dialog modal ${className}`.trim()}
      aria-label={title}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      {open && (
        <>
          <div className="modal-head">
            <h2>{title}</h2>
            <button type="button" className="btn ghost small" onClick={onClose} aria-label="Close">
              ×
            </button>
          </div>
          {children}
        </>
      )}
    </dialog>
  );
}
