// The answer panel for a pending ask_user gate (single or batch). It takes
// the composer's place while a question is open. A batch is a stepper: one
// question at a time, picking an option moves on to the next unanswered one,
// and one Send submits them all. It is a projection of durable
// SessionViewState: it stays up after submitting until the daemon reports the
// question closed — which also covers an answer given from another client.
import { useRef, useState, type KeyboardEvent } from "react";
import type { PendingQuestion } from "./projection";
import type { SessionController } from "./controller";
import { track, type Via } from "../../app/analytics";
import { IS_MAC } from "../../app/platform";
import { Markdown } from "../markdown/Markdown";

const MOD = IS_MAC ? "⌘" : "Ctrl+";

export function AnswerPanel({
  controller,
  question,
  inFlight,
  submitted,
}: {
  controller: SessionController;
  question: PendingQuestion;
  inFlight: boolean;
  submitted: boolean;
}) {
  const qs = question.questions;
  const batch = qs.length > 1;
  const [current, setCurrent] = useState(0);
  const [choices, setChoices] = useState<number[]>(() => qs.map(() => -1));
  const [texts, setTexts] = useState<string[]>(() => qs.map(() => ""));
  const panel = useRef<HTMLElement>(null);
  const disabled = inFlight || submitted;
  const answered = qs.map((q, i) => !!texts[i].trim() || (choices[i] >= 0 && choices[i] < q.options.length));
  const complete = answered.every(Boolean);
  const remaining = answered.filter((a) => !a).length;

  /** Focus question i's first control once it is shown (option, else text). */
  const focusQuestion = (i: number) =>
    requestAnimationFrame(() => {
      const q = panel.current?.querySelector<HTMLElement>(`[data-q="${i}"]`);
      (q?.querySelector<HTMLElement>(".opt.selected, .opt, textarea") ?? null)?.focus();
    });
  const go = (i: number) => {
    setCurrent(i);
    focusQuestion(i);
  };
  /** After answering question i: the next unanswered question, else the Send button. */
  const advance = (i: number, nowAnswered: boolean[]) => {
    for (let step = 1; step < qs.length; step++) {
      const j = (i + step) % qs.length;
      if (!nowAnswered[j]) return go(j);
    }
    requestAnimationFrame(() => panel.current?.querySelector<HTMLElement>(".answer-send")?.focus());
  };

  const sendSingleText = (via: Via) => {
    const t = texts[0].trim();
    if (disabled || !t) return;
    track.action("question.answer", via, { kind: "text", options: qs[0].options.length > 0 });
    void controller.answerText(t);
  };

  const sendBatch = (via: Via) => {
    if (disabled || !complete) return;
    track.action("question.answer", via, { kind: "batch", count: Math.min(qs.length, 10) });
    void controller.answerBatch(
      qs.map((q, i) => {
        const option = choices[i];
        if (option >= 0 && option < q.options.length && !texts[i].trim()) return { text: "", optionIndex: option };
        return { text: texts[i].trim(), optionIndex: -1 };
      }),
    );
  };

  const pick = (qi: number, oi: number, via: Via) => {
    if (disabled || oi >= qs[qi].options.length) return;
    if (!batch) {
      track.action("question.answer", via, { kind: "option" });
      void controller.answerOption(oi);
      return;
    }
    setChoices((c) => c.map((v, i) => (i === qi ? oi : v)));
    setTexts((t) => t.map((v, i) => (i === qi ? "" : v)));
    advance(qi, answered.map((a, i) => (i === qi ? true : a)));
  };

  const onKeyDown = (e: KeyboardEvent) => {
    if (disabled || e.nativeEvent.isComposing) return;
    const inText = e.target instanceof HTMLTextAreaElement;
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (batch) sendBatch("keyboard");
      else sendSingleText("keyboard");
      return;
    }
    if (inText && e.key === "Enter" && !e.shiftKey && !e.altKey) {
      e.preventDefault();
      if (!batch) sendSingleText("keyboard");
      else if (complete) sendBatch("keyboard");
      else if (answered[current]) advance(current, answered);
      return;
    }
    if (inText || e.altKey || e.metaKey || e.ctrlKey) return;
    if (/^[1-9]$/.test(e.key)) {
      e.preventDefault();
      pick(current, Number(e.key) - 1, "keyboard");
    } else if (batch && (e.key === "ArrowRight" || e.key === "ArrowLeft")) {
      e.preventDefault();
      go((current + (e.key === "ArrowRight" ? 1 : qs.length - 1)) % qs.length);
    }
  };

  return (
    <section ref={panel} className="answer-panel" aria-label="Answer the agent's question" onKeyDown={onKeyDown}>
      <div className={`answer-card${submitted ? " submitted" : ""}`}>
        <div className="answer-head">
          <span className="answer-badge" aria-hidden="true">?</span>
          <span className="answer-title">
            {batch ? `The agent asks ${qs.length} questions` : "The agent asks"}
          </span>
          {submitted ? (
            <span className="tag">answer sent — waiting for the session…</span>
          ) : (
            batch && (
              <nav className="answer-steps" aria-label="Questions">
                {qs.map((q, i) => (
                  <button
                    key={i}
                    type="button"
                    className={`answer-step${i === current ? " current" : ""}${answered[i] ? " done" : ""}`}
                    aria-current={i === current ? "step" : undefined}
                    aria-label={`Question ${i + 1}${answered[i] ? " (answered)" : ""}`}
                    title={q.prompt.length > 120 ? `${q.prompt.slice(0, 117)}…` : q.prompt}
                    onClick={() => go(i)}
                  >
                    {answered[i] && i !== current ? "✓" : i + 1}
                  </button>
                ))}
              </nav>
            )
          )}
        </div>
        {qs.map((q, qi) => (
          <div key={qi} className="answer-q" data-q={qi} hidden={qi !== current}>
            <div className="answer-scroll">
              {batch && (
                <div className="answer-count">
                  Question {qi + 1} of {qs.length}
                </div>
              )}
              <div className="answer-prompt">
                <Markdown text={q.prompt} />
              </div>
              {q.options.length > 0 && (
                <div className="answer-options" role="group" aria-label={`Options for question ${qi + 1}`}>
                  {q.options.map((o, oi) => {
                    const selected = choices[qi] === oi && !texts[qi].trim();
                    return (
                      <button
                        key={oi}
                        type="button"
                        className={`opt${selected ? " selected" : ""}`}
                        disabled={disabled}
                        aria-pressed={batch ? selected : undefined}
                        onClick={() => pick(qi, oi, "click")}
                      >
                        {oi < 9 && <kbd aria-hidden="true">{oi + 1}</kbd>}
                        <span className="opt-label">{o}</span>
                        {selected && <span className="opt-check" aria-hidden="true">✓</span>}
                      </button>
                    );
                  })}
                </div>
              )}
            </div>
            <textarea
              rows={1}
              className={`answer-text${texts[qi].trim() ? " filled" : ""}`}
              placeholder={q.options.length ? "Or write your own answer…" : "Type your answer…"}
              aria-label={`Answer to question ${qi + 1}`}
              value={texts[qi]}
              disabled={disabled}
              onChange={(e) => {
                const v = e.target.value;
                setTexts((t) => t.map((x, i) => (i === qi ? v : x)));
              }}
            />
          </div>
        ))}
        <div className="answer-actions">
          <span className="answer-hint muted small">
            {qs[current].options.length > 0 && (
              <>
                <kbd>1</kbd>–<kbd>{Math.min(9, qs[current].options.length)}</kbd> pick ·{" "}
              </>
            )}
            {batch ? (
              <>
                <kbd>Enter</kbd> next · <kbd>{MOD}Enter</kbd> send
              </>
            ) : (
              <>
                <kbd>Enter</kbd> send · <kbd>⇧Enter</kbd> new line
              </>
            )}
          </span>
          {batch && current > 0 && (
            <button type="button" className="btn" disabled={submitted} onClick={() => go(current - 1)}>
              Back
            </button>
          )}
          {batch && current + 1 < qs.length && (
            <button type="button" className="btn" disabled={submitted} onClick={() => go(current + 1)}>
              Next
            </button>
          )}
          {batch ? (
            <button
              type="button"
              className="btn primary answer-send"
              disabled={disabled || !complete}
              title={complete ? undefined : `${remaining} question${remaining === 1 ? "" : "s"} still need an answer`}
              onClick={() => sendBatch("click")}
            >
              {inFlight ? "Sending…" : complete ? `Send ${qs.length} answers` : `${remaining} left`}
            </button>
          ) : (
            <button
              type="button"
              className="btn primary answer-send"
              disabled={disabled || !texts[0].trim()}
              onClick={() => sendSingleText("click")}
            >
              {inFlight ? "Sending…" : "Send answer"}
            </button>
          )}
        </div>
      </div>
    </section>
  );
}
