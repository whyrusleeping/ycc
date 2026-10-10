// The answer panel for a pending ask_user gate (single or batch). It is a
// projection of durable SessionViewState: it stays up after submitting until
// the daemon reports the question closed — which also covers an answer given
// from another client.
import { useState } from "react";
import type { PendingQuestion } from "./projection";
import type { SessionController } from "./controller";
import { track, type Via } from "../../app/analytics";

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
  const batch = question.questions.length > 1;
  const [choices, setChoices] = useState<number[]>(() => question.questions.map(() => -1));
  const [texts, setTexts] = useState<string[]>(() => question.questions.map(() => ""));
  const disabled = inFlight || submitted;
  const complete = question.questions.every((q, i) =>
    !!texts[i].trim() || (choices[i] >= 0 && choices[i] < q.options.length),
  );

  const sendSingleText = (via: Via) => {
    const t = texts[0].trim();
    if (!t) return;
    track.action("question.answer", via, { kind: "text", options: question.questions[0].options.length > 0 });
    void controller.answerText(t);
  };

  const sendBatch = () => {
    if (disabled || !complete) return;
    track.action("question.answer", "click", { kind: "batch", count: Math.min(question.questions.length, 10) });
    void controller.answerBatch(
      question.questions.map((q, i) => {
        const option = choices[i];
        if (option >= 0 && option < q.options.length && !texts[i].trim()) return { text: "", optionIndex: option };
        return { text: texts[i].trim(), optionIndex: -1 };
      }),
    );
  };

  return (
    <section className="answer-panel" aria-label="Answer the agent's question">
      <div className="answer-head">
        {batch ? `The agent asks ${question.questions.length} questions` : "The agent asks"}
        {submitted && <span className="tag">answer sent — waiting for the session…</span>}
      </div>
      {question.questions.map((q, qi) => (
        <div key={qi} className="answer-q">
          <div className="text prompt">{q.prompt}</div>
          {q.options.length > 0 && (
            <div className="answer-options" role="group" aria-label={`Options for: ${q.prompt}`}>
              {q.options.map((o, oi) => (
                <button
                  key={oi}
                  type="button"
                  className={`opt${choices[qi] === oi && !texts[qi].trim() ? " selected" : ""}`}
                  disabled={disabled}
                  aria-pressed={batch ? choices[qi] === oi && !texts[qi].trim() : undefined}
                  onClick={() => {
                    if (batch) {
                      setChoices((c) => c.map((v, i) => (i === qi ? oi : v)));
                      setTexts((t) => t.map((v, i) => (i === qi ? "" : v)));
                    } else {
                      track.action("question.answer", "click", { kind: "option" });
                      void controller.answerOption(oi);
                    }
                  }}
                >
                  {o}
                </button>
              ))}
            </div>
          )}
          <textarea
            rows={1}
            className="answer-text"
            placeholder={q.options.length ? "Or type an answer…" : "Type an answer…"}
            aria-label={`Answer to: ${q.prompt}`}
            value={texts[qi]}
            disabled={disabled}
            onChange={(e) => {
              const v = e.target.value;
              setTexts((t) => t.map((x, i) => (i === qi ? v : x)));
            }}
            onKeyDown={(e) => {
              if (!batch && e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                sendSingleText("keyboard");
              }
            }}
          />
        </div>
      ))}
      <div className="answer-actions">
        {batch ? (
          <button type="button" className="btn primary" disabled={disabled || !complete} onClick={sendBatch}>
            {inFlight ? "Sending…" : "Send answers"}
          </button>
        ) : (
          <button
            type="button"
            className="btn primary"
            disabled={disabled || !texts[0].trim()}
            onClick={() => sendSingleText("click")}
          >
            {inFlight ? "Sending…" : "Send answer"}
          </button>
        )}
      </div>
    </section>
  );
}
