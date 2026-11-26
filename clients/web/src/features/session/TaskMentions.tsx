// `#123` task mentions in a composer textarea: while the caret sits in a
// `#digits` token, a popover above the message box lists the backlog tasks it
// may name (id, title, status). ↑/↓ move, Enter or Tab inserts `#0613 `,
// Escape dismisses it until the caret leaves the token; a click also inserts.
import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type RefObject } from "react";
import type { BacklogTaskSummary } from "../../gen/ycc/v1/ycc_pb";
import { StatusPill } from "../backlog/parts";
import { applyMention, matchTasks, mentionAt } from "./mentions";

export interface TaskMentionSource {
  /** The project's backlog, undefined while it loads. */
  tasks: readonly BacklogTaskSummary[] | undefined;
  /** Called once a `#digits` token first appears (so the backlog can load lazily). */
  onWanted?: () => void;
}

export function useTaskMentions(
  area: RefObject<HTMLTextAreaElement | null>,
  text: string,
  setText: (text: string) => void,
  source: TaskMentionSource | undefined,
) {
  const listId = useId();
  const [caret, setCaret] = useState<number | null>(null);
  const [cursor, setCursor] = useState(0);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const pendingCaret = useRef<number | null>(null);

  const token = source && caret !== null ? mentionAt(text, caret) : null;
  const matches = token && source?.tasks ? matchTasks(source.tasks, token.digits) : [];
  const shown = !!token && dismissed !== token.start;
  const open = shown && matches.length > 0;
  const active = Math.min(cursor, Math.max(0, matches.length - 1));

  const wanted = !!token;
  const onWanted = source?.onWanted;
  useEffect(() => {
    if (wanted) onWanted?.();
  }, [wanted, onWanted]);

  // A new token or more digits start at the top again.
  const key = token ? `${token.start}:${token.digits}` : "";
  useEffect(() => setCursor(0), [key]);
  useEffect(() => {
    if (!wanted) setDismissed(null);
  }, [wanted]);

  // Put the caret after an inserted mention once React has written the text.
  useLayoutEffect(() => {
    const el = area.current;
    const at = pendingCaret.current;
    if (!el || at === null) return;
    pendingCaret.current = null;
    el.setSelectionRange(at, at);
    setCaret(at);
  }, [text, area]);

  const pick = (id: string) => {
    if (!token) return;
    const next = applyMention(text, token, id);
    pendingCaret.current = next.caret;
    setText(next.text);
  };

  /** Track the caret (onChange, onSelect, onFocus). */
  const sync = (el: HTMLTextAreaElement) => setCaret(el.selectionStart === el.selectionEnd ? el.selectionStart : null);

  /** The composer's keydown, first: true when the popover consumed the key. */
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>): boolean => {
    if (!open || e.nativeEvent.isComposing || e.altKey || e.ctrlKey || e.metaKey) return false;
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp": {
        const n = matches.length;
        setCursor((active + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
        break;
      }
      case "Enter":
      case "Tab":
        if (e.shiftKey) return false;
        pick(matches[active].id);
        break;
      case "Escape":
        setDismissed(token!.start);
        break;
      default:
        return false;
    }
    e.preventDefault();
    e.stopPropagation();
    return true;
  };

  const inputProps = {
    "aria-autocomplete": "list" as const,
    "aria-expanded": open,
    "aria-controls": open ? listId : undefined,
    "aria-activedescendant": open ? `${listId}-${active}` : undefined,
    onSelect: (e: { currentTarget: HTMLTextAreaElement }) => sync(e.currentTarget),
    onFocus: (e: { currentTarget: HTMLTextAreaElement }) => sync(e.currentTarget),
    onBlur: () => setCaret(null),
  };

  const popover = !shown ? null : open ? (
    <ul className="task-mentions" id={listId} role="listbox" aria-label="Backlog tasks">
      {matches.map((t, i) => (
        <li
          key={t.id}
          id={`${listId}-${i}`}
          role="option"
          aria-selected={i === active}
          className={`task-mention${i === active ? " cursor" : ""}`}
          // Keep focus (and the caret) in the textarea.
          onMouseDown={(e) => e.preventDefault()}
          onMouseEnter={() => setCursor(i)}
          onClick={() => pick(t.id)}
        >
          <span className="task-mention-id">#{t.id}</span>
          <span className="task-mention-title">{t.title}</span>
          <StatusPill status={t.status} />
        </li>
      ))}
    </ul>
  ) : source?.tasks === undefined ? (
    <div className="task-mentions muted small">Loading tasks…</div>
  ) : null;

  return { onKeyDown, sync, inputProps, popover };
}
