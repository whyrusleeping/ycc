// A small modal confirmation built on <dialog>.
import { useEffect, useRef } from "react";
import { track } from "../app/analytics";

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel,
  danger,
  onConfirm,
  onCancel,
  action,
}: {
  open: boolean;
  title: string;
  body: string;
  confirmLabel: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  /** Analytics action id of what is being confirmed: records `<action>.confirm` or `.cancel`. */
  action?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const confirm = () => {
    if (action) track.action(`${action}.confirm`, "click");
    onConfirm();
  };
  const cancel = () => {
    if (action) track.action(`${action}.cancel`);
    onCancel();
  };
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) d.showModal();
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      className="dialog"
      onCancel={(e) => {
        e.preventDefault();
        cancel();
      }}
    >
      <h2>{title}</h2>
      <p>{body}</p>
      <div className="dialog-actions">
        <button type="button" className="btn ghost" onClick={cancel}>
          Cancel
        </button>
        <button type="button" className={`btn ${danger ? "danger" : "primary"}`} onClick={confirm} autoFocus>
          {confirmLabel}
        </button>
      </div>
    </dialog>
  );
}
