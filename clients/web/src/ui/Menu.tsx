// A small popup menu behind a button (row kebabs, the sidebar's project
// actions). Closes on outside click, Escape, or after an item runs.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { track } from "../app/analytics";

export interface MenuItem {
  /** Analytics action id (fixed vocabulary, e.g. "projects.rename"); labels may hold names. */
  id?: string;
  label: string;
  onSelect: () => void;
  danger?: boolean;
  disabled?: boolean;
  title?: string;
  /** A keyboard-shortcut hint shown at the item's right edge, e.g. "Alt+N". */
  shortcut?: string;
}

export function MenuButton({
  label,
  ariaLabel,
  items,
  className = "btn ghost small",
  align = "right",
  title,
}: {
  label: ReactNode;
  ariaLabel: string;
  items: readonly (MenuItem | null | false)[];
  className?: string;
  align?: "left" | "right";
  /** The button's tooltip (default: ariaLabel). */
  title?: string;
}) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);
  const shown = items.filter((i): i is MenuItem => !!i);
  return (
    <div className="menu-wrap" ref={wrap}>
      <button
        type="button"
        className={className}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        title={title ?? ariaLabel}
        onClick={(e) => {
          e.stopPropagation();
          setOpen((o) => !o);
        }}
      >
        {label}
      </button>
      {open && (
        <div className={`menu${align === "left" ? " align-left" : ""}`} role="menu">
          {shown.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className={item.danger ? "danger" : undefined}
              disabled={item.disabled}
              title={item.title}
              onClick={(e) => {
                e.stopPropagation();
                setOpen(false);
                if (item.id) track.action(item.id, "menu");
                item.onSelect();
              }}
            >
              {item.label}
              {item.shortcut && <kbd>{item.shortcut}</kbd>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
