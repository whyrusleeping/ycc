// Multiline composer: Enter sends, Shift+Enter inserts a newline. Pictures can
// be attached with the paperclip, pasted, or dropped onto the session view.
// Drafts (text and pictures) are kept per session while the page is open.
import { forwardRef, useEffect, useImperativeHandle, useRef, useState } from "react";
import type { DraftPicture } from "../attachments/attachments";
import { AttachButton, PictureStrip, filesFrom, usePictureDraft } from "../attachments/pictures";
import { track, type Attrs, type Via } from "../../app/analytics";

const drafts = new Map<string, string>();
const pictureDrafts = new Map<string, DraftPicture[]>();

export interface ComposerHandle {
  setDraft: (text: string, pictures?: DraftPicture[]) => void;
  /** Stage dropped files (the drop zone is the whole session view). */
  addFiles: (files: File[]) => void;
}

export const Composer = forwardRef<
  ComposerHandle,
  {
    sessionKey: string;
    placeholder: string;
    onSend: (text: string, pictures: DraftPicture[]) => void;
    disabled?: boolean;
    /** Why staged pictures can't be sent right now (e.g. a question is pending). */
    picturesBlocked?: string;
    /** Enum-valued analytics attrs for `composer.send` (e.g. the session phase). */
    sendAttrs?: Attrs;
  }
>(function Composer({ sessionKey, placeholder, onSend, disabled, picturesBlocked, sendAttrs }, ref) {
  const [text, setText] = useState(() => drafts.get(sessionKey) ?? "");
  const area = useRef<HTMLTextAreaElement>(null);
  const draft = usePictureDraft(pictureDrafts.get(sessionKey) ?? [], (next) => {
    if (next.length) pictureDrafts.set(sessionKey, next);
    else pictureDrafts.delete(sessionKey);
  });

  useImperativeHandle(ref, () => ({
    setDraft: (t: string, pictures?: DraftPicture[]) => {
      setText(t);
      if (pictures?.length) draft.replace([...draft.pictures, ...pictures]);
      area.current?.focus();
    },
    addFiles: (files: File[]) => {
      track.action("composer.attach_image", "gesture", { source: "drop" });
      void draft.add(files);
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

  const hasPictures = draft.pictures.length > 0;
  const canSend = !disabled && !draft.loading && (text.trim() !== "" || hasPictures);

  const submit = (via: Via) => {
    if (!canSend) return;
    track.action("composer.send", via, { ...sendAttrs, pictures: hasPictures, text: text.trim() !== "" });
    onSend(text.trim(), draft.pictures);
    setText("");
    // The previews now belong to the provisional bubble.
    draft.release();
  };

  return (
    <form
      className="composer"
      onSubmit={(e) => {
        e.preventDefault();
        submit("click");
      }}
    >
      {(hasPictures || draft.error) && (
        <div className="composer-pictures">
          <PictureStrip pictures={draft.pictures} onRemove={draft.remove} />
          {draft.error && <p className="error small">{draft.error}</p>}
          {picturesBlocked && hasPictures && <p className="warn small">{picturesBlocked}</p>}
        </div>
      )}
      <div className="composer-row">
        <AttachButton
          onFiles={(f) => {
            track.action("composer.attach_image", "click", { source: "button" });
            void draft.add(f);
          }}
          disabled={disabled}
          full={draft.full}
        />
        <textarea
          ref={area}
          rows={1}
          value={text}
          placeholder={placeholder}
          aria-label="Message"
          onChange={(e) => setText(e.target.value)}
          onPaste={(e) => {
            const files = filesFrom(e.clipboardData).filter((f) => f.type.startsWith("image/"));
            if (!files.length) return;
            e.preventDefault();
            track.action("composer.attach_image", "keyboard", { source: "paste" });
            void draft.add(files);
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit("keyboard");
            }
          }}
        />
        <button type="submit" className="btn primary" disabled={!canSend}>
          {draft.loading ? "Attaching…" : "Send"}
        </button>
      </div>
    </form>
  );
});
