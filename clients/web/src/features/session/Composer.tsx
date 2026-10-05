// Multiline composer: Enter sends, Shift+Enter inserts a newline. Drafts are
// kept per session while the page is open.
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";

const drafts = new Map<string, string>();

export interface ComposerHandle {
  setText: (text: string) => void;
}

export const Composer = forwardRef<
  ComposerHandle,
  { sessionKey: string; placeholder: string; onSend: (text: string) => void; disabled?: boolean }
>(function Composer({ sessionKey, placeholder, onSend, disabled }, ref) {
  const [text, setText] = useState(() => drafts.get(sessionKey) ?? "");
  const area = useRef<HTMLTextAreaElement>(null);

  useImperativeHandle(ref, () => ({
    setText: (t: string) => {
      setText(t);
      area.current?.focus();
    },
  }));

  useEffect(() => {
    drafts.set(sessionKey, text);
  }, [sessionKey, text]);

  useEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 260)}px`;
  }, [text]);

  const submit = () => {
    const t = text.trim();
    if (!t || disabled) return;
    onSend(t);
    setText("");
  };

  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <textarea
        ref={area}
        rows={1}
        value={text}
        placeholder={placeholder}
        aria-label="Message"
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            submit();
          }
        }}
      />
      <button type="submit" className="btn primary" disabled={disabled || !text.trim()}>
        Send
      </button>
    </form>
  );
});
