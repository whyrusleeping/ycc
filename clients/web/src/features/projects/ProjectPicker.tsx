// The sidebar's project picker: a listbox popup (a native <select> can't show
// badges) listing every project with its live activity — waiting questions,
// unread agent output, and work in flight — as the iOS workspace drawer does.
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { activityDescription, hasActivity, type Activity } from "../sessions/activity";

export interface ProjectOption {
  /** null is the daemon-wide Recent feed. */
  value: string | null;
  label: string;
  activity?: Activity;
  /** The quiet git sync badge (gitSyncBadge) and its tooltip. */
  git?: string | null;
  gitTitle?: string;
}

/** Waiting (loudest), unread, then active — the iOS drawer's badge order. */
export function ActivityBadges({ activity }: { activity: Activity | undefined }) {
  if (!activity || !hasActivity(activity)) return null;
  return (
    <span className="activity-badges" aria-hidden="true">
      {activity.needsAnswer > 0 && (
        <span className="activity-badge waiting" title={`${activity.needsAnswer} waiting for an answer`}>
          {activity.needsAnswer}
        </span>
      )}
      {activity.unread > 0 && (
        <span className="activity-badge unread" title={`${activity.unread} with unread agent activity`}>
          {activity.unread}
        </span>
      )}
      {activity.active > 0 && (
        <span className="activity-badge active" title={`${activity.active} active`}>
          {activity.active}
        </span>
      )}
    </span>
  );
}

function optionLabel(o: ProjectOption): string {
  const what = activityDescription(o.activity);
  return what ? `${o.label}: ${what}` : o.label;
}

export function ProjectPicker({
  options,
  value,
  onChange,
}: {
  options: readonly ProjectOption[];
  value: string | null;
  onChange: (value: string | null) => void;
}) {
  const [open, setOpen] = useState(false);
  const [cursor, setCursor] = useState(0);
  const wrap = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const selected = Math.max(0, options.findIndex((o) => o.value === value));
  const current = options[selected];

  useEffect(() => {
    if (!open) return;
    list.current?.focus();
    const onDown = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  useEffect(() => {
    if (open) list.current?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }, [open, cursor]);

  const show = (at = selected) => {
    setCursor(at);
    setOpen(true);
  };
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) button.current?.focus();
  };
  const choose = (i: number) => {
    close(true);
    const o = options[i];
    if (o && o.value !== value) onChange(o.value);
  };

  const onListKey = (e: KeyboardEvent) => {
    const last = options.length - 1;
    const move = (i: number) => {
      e.preventDefault();
      setCursor(Math.min(last, Math.max(0, i)));
    };
    switch (e.key) {
      case "ArrowDown":
        return move(cursor + 1);
      case "ArrowUp":
        return move(cursor - 1);
      case "Home":
      case "PageUp":
        return move(0);
      case "End":
      case "PageDown":
        return move(last);
      case "Enter":
      case " ":
        e.preventDefault();
        return choose(cursor);
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        return close(true);
      case "Tab":
        return close(false);
    }
    // Type-ahead: jump to the next project starting with the typed letter.
    // Printable keys stop here so page shortcuts (j/k, ?) don't fire behind the open list.
    if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      e.preventDefault();
      const ch = e.key.toLowerCase();
      for (let step = 1; step <= options.length; step++) {
        const i = (cursor + step) % options.length;
        if (options[i].label.toLowerCase().startsWith(ch)) return move(i);
      }
    }
  };

  return (
    <div className="project-picker" ref={wrap}>
      <button
        ref={button}
        type="button"
        className="project-picker-btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`Project: ${current ? optionLabel(current) : "All projects"}`}
        title={current && hasActivity(current.activity) ? optionLabel(current) : undefined}
        onClick={() => (open ? close(false) : show())}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            e.preventDefault();
            show();
          }
        }}
      >
        <span className="project-picker-name">{current?.label ?? "All projects"}</span>
        <ActivityBadges activity={current?.activity} />
      </button>
      {open && (
        <ul
          ref={list}
          className="project-picker-list"
          role="listbox"
          aria-label="Projects"
          tabIndex={-1}
          aria-activedescendant={`project-option-${cursor}`}
          onKeyDown={onListKey}
        >
          {options.map((o, i) => (
            <li
              key={o.value ?? ""}
              id={`project-option-${i}`}
              data-index={i}
              role="option"
              aria-selected={i === selected}
              aria-label={optionLabel(o)}
              className={`project-option${i === cursor ? " cursor" : ""}${i === selected ? " selected" : ""}${o.value === null ? " all" : ""}`}
              onMouseMove={() => i !== cursor && setCursor(i)}
              onClick={() => choose(i)}
            >
              <span className="project-option-name">{o.label}</span>
              {o.git && (
                <span className="project-option-git" title={o.gitTitle}>
                  {o.git}
                </span>
              )}
              <ActivityBadges activity={o.activity} />
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
