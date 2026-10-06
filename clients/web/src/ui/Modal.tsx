// A modal surface built on <dialog> (focus trap, Esc, and backdrop from the
// platform). Esc calls onClose; the caller decides whether closing is allowed.
import { useEffect, useRef, type ReactNode } from "react";

export function Modal({
  open,
  onClose,
  title,
  className = "",
  children,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  className?: string;
  children: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
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
