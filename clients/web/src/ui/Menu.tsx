// A small popup menu behind a button (row kebabs, the sidebar's project
// actions). Closes on outside click, Escape, or after an item runs.
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
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
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const openAtEnd = useRef(false);
  const menuId = useId();
  const enabledItems = () => Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []);
  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  useEffect(() => {
    if (!open) return;
    const buttons = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? []);
    (openAtEnd.current ? buttons.at(-1) : buttons[0])?.focus();
    if (!buttons.length) menu.current?.focus();
    const onDown = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  const shown = items.filter((i): i is MenuItem => !!i);
  return (
    <div
      className="menu-wrap"
      ref={wrap}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
      }}
      onKeyDown={(e) => {
        if (!open) return;
        if (e.key === "Tab") {
          // Let native Tab move from the trigger to the next/previous control,
          // rather than removing the focused item before the browser moves it.
          trigger.current?.focus();
          setOpen(false);
          return;
        }
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          close();
          return;
        }
        if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) return;
        e.preventDefault();
        e.stopPropagation();
        const buttons = enabledItems();
        if (!buttons.length) return;
        const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
        const next = e.key === "Home" ? 0 : e.key === "End" ? buttons.length - 1
          : (current + (e.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
        buttons[next].focus();
      }}
    >
      <button
        ref={trigger}
        type="button"
        className={className}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-label={ariaLabel}
        title={title ?? ariaLabel}
        onClick={(e) => {
          e.stopPropagation();
          if (open) close();
          else {
            openAtEnd.current = false;
            setOpen(true);
          }
        }}
        onKeyDown={(e) => {
          if (open || (e.key !== "ArrowDown" && e.key !== "ArrowUp")) return;
          e.preventDefault();
          e.stopPropagation();
          openAtEnd.current = e.key === "ArrowUp";
          setOpen(true);
        }}
      >
        {label}
      </button>
      {open && (
        <div ref={menu} id={menuId} className={`menu${align === "left" ? " align-left" : ""}`} role="menu" aria-label={ariaLabel} tabIndex={-1}>
          {shown.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              tabIndex={-1}
              className={item.danger ? "danger" : undefined}
              disabled={item.disabled}
              title={item.title}
              onClick={(e) => {
                e.stopPropagation();
                close();
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
