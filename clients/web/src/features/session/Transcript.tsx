// The scrolling transcript: follows the live edge while the reader is at the
// bottom, never moves a reader who scrolled away (a pill offers a jump to the
// latest), and pages earlier rows when scrolled to the top while keeping the
// viewport anchored.
import { useLayoutEffect, useRef, useState } from "react";
import type { SessionController, SessionSnapshot } from "./controller";
import { RowView } from "./RowView";

const NEAR_BOTTOM_PX = 80;
const NEAR_TOP_PX = 200;

export function Transcript({
  controller,
  snap,
  onEditFailed,
}: {
  controller: SessionController;
  snap: SessionSnapshot;
  onEditFailed: (text: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  const lastHeight = useRef(0);
  const lastEarlier = useRef(snap.earlierRevision);
  const lastInstall = useRef(-1);
  const [showPill, setShowPill] = useState(false);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (snap.installRevision !== lastInstall.current) {
      lastInstall.current = snap.installRevision;
      lastEarlier.current = snap.earlierRevision;
      following.current = true;
    }
    if (snap.earlierRevision !== lastEarlier.current) {
      // Earlier rows were prepended: keep the same content under the reader.
      lastEarlier.current = snap.earlierRevision;
      el.scrollTop += el.scrollHeight - lastHeight.current;
    } else if (following.current) {
      el.scrollTop = el.scrollHeight;
    } else if (el.scrollHeight > lastHeight.current + 1) {
      setShowPill(true);
    }
    lastHeight.current = el.scrollHeight;
  });

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    following.current = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    if (following.current && showPill) setShowPill(false);
    if (el.scrollTop < NEAR_TOP_PX && snap.hasEarlier && !snap.loadingEarlier) void controller.loadEarlier();
  };

  const jump = () => {
    const el = scroller.current;
    if (!el) return;
    following.current = true;
    el.scrollTop = el.scrollHeight;
    setShowPill(false);
  };

  const working =
    snap.mode === "live" &&
    snap.conn === "streaming" &&
    snap.phase.kind === "running" &&
    !snap.pauseRequested &&
    !snap.rows.some((r) => r.kind.type === "liveTail");

  return (
    <div className="transcript-wrap">
      <div className="transcript" ref={scroller} onScroll={onScroll}>
        <div className="transcript-inner">
          {snap.hasEarlier ? (
            <div className="earlier">
              <button
                type="button"
                className="btn ghost small"
                disabled={snap.loadingEarlier}
                onClick={() => void controller.loadEarlier()}
              >
                {snap.loadingEarlier ? "Loading earlier…" : "Load earlier"}
              </button>
            </div>
          ) : (
            snap.installed && <div className="earlier muted">Start of session</div>
          )}
          {snap.installed && snap.rows.length === 0 && <p className="muted pad">No events yet.</p>}
          {snap.rows.map((row) => (
            <RowView
              key={row.id}
              row={row}
              controller={controller}
              loadingDetail={snap.loadingDetail.has(row.id)}
            />
          ))}
          {snap.pendingMessages.map((m) => (
            <div key={m.id} className={`row turn user pending ${m.status}`}>
              <div className="turn-head">
                <span className="who">You</span>
                <span className={m.status === "failed" ? "tag error" : "tag"}>
                  {m.status === "sending" ? "sending…" : m.status === "sent" ? "sent" : "not sent"}
                </span>
              </div>
              <div className="text">{m.text}</div>
              {m.status === "failed" && (
                <div className="row-actions">
                  {m.error && <span className="error">{m.error}</span>}
                  <button type="button" className="link" onClick={() => void controller.retrySend(m.id)}>
                    Retry
                  </button>
                  <button
                    type="button"
                    className="link"
                    onClick={() => {
                      const text = controller.discardSend(m.id);
                      if (text !== undefined) onEditFailed(text);
                    }}
                  >
                    Edit
                  </button>
                </div>
              )}
            </div>
          ))}
          {working && <div className="working muted">Working…</div>}
          {snap.mode === "live" && snap.awaitingJobs && (
            <div className="working muted">Waiting on background jobs…</div>
          )}
        </div>
      </div>
      {showPill && (
        <button type="button" className="pill" onClick={jump}>
          ↓ Jump to latest
        </button>
      )}
    </div>
  );
}
