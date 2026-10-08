// The scrolling transcript: follows the live edge while the reader is at the
// bottom, never moves a reader who scrolled away (a pill offers a jump to the
// latest), and pages earlier rows when scrolled to the top while keeping the
// viewport anchored. Transcript search marks matching rows, highlights the
// matched text (CSS Custom Highlight API, no DOM mutation), and scrolls the
// current match into view. Long runs of tool/reasoning rows fold into an
// activity summary (see blocks.ts) that the reader can expand in place.
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import type { SessionController, SessionSnapshot } from "./controller";
import { clickVia, track } from "../../app/analytics";
import { RowView } from "./RowView";
import type { DraftPicture } from "../attachments/attachments";
import { reportPresentation } from "./report";
import { SearchBar, type TranscriptSearch } from "./SearchBar";
import { activitySummary, blockHides, compactDuration, foldedTailStart, transcriptBlocks, type ActivitySummary } from "./blocks";

const NEAR_BOTTOM_PX = 80;
const NEAR_TOP_PX = 200;

export function Transcript({
  controller,
  snap,
  onEditFailed,
  search,
}: {
  controller: SessionController;
  snap: SessionSnapshot;
  onEditFailed: (text: string, pictures: DraftPicture[]) => void;
  search?: TranscriptSearch;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const lastSearchToken = useRef(search?.token ?? 0);
  const following = useRef(true);
  const lastHeight = useRef(0);
  const lastScrollTop = useRef(0);
  const lastEarlier = useRef(snap.earlierRevision);
  const lastInstall = useRef(-1);
  const [showPill, setShowPill] = useState(false);
  // Activity blocks the reader expanded (keyed by their first row id).
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  // Growth the reader asked for (expanding a block) is not "new activity".
  const quietGrowth = useRef(false);

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
      lastScrollTop.current = el.scrollTop;
    } else if (following.current) {
      el.scrollTop = el.scrollHeight;
      lastScrollTop.current = el.scrollTop;
    } else if (el.scrollHeight > lastHeight.current + 1 && !quietGrowth.current) {
      setShowPill(true);
    }
    quietGrowth.current = false;
    if (search && search.token !== lastSearchToken.current) {
      lastSearchToken.current = search.token;
      if (search.currentRowId && scrollToRow(el, search.currentRowId)) {
        following.current = false;
        lastScrollTop.current = el.scrollTop;
      }
    }
    lastHeight.current = el.scrollHeight;
  });

  useLayoutEffect(() => {
    const el = scroller.current;
    const content = inner.current;
    if (!el || !content) return;
    // Live-tail pacing (and other in-row layout changes) does not render the
    // Transcript itself. React's layout effect above still owns prepend anchoring
    // and runs before resize notifications, so these never apply that delta twice.
    const observer = new ResizeObserver(() => {
      if (following.current) {
        el.scrollTop = el.scrollHeight;
        lastScrollTop.current = el.scrollTop;
      } else if (el.scrollHeight > lastHeight.current + 1) {
        setShowPill(true);
      }
      lastHeight.current = el.scrollHeight;
    });
    observer.observe(content);
    return () => observer.disconnect();
  }, []);

  const needle = search?.needle ?? "";
  const currentId = search?.currentRowId ?? null;
  useEffect(() => {
    const root = inner.current;
    if (!root || !needle) {
      clearHighlights();
      return;
    }
    let frame = 0;
    const schedule = () => {
      if (frame) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        applyHighlights(root, needle, currentId);
      });
    };
    schedule();
    // Rows re-render, folds open, and text streams in: keep highlights current.
    const observer = new MutationObserver(schedule);
    observer.observe(root, { childList: true, subtree: true, characterData: true });
    return () => {
      observer.disconnect();
      if (frame) cancelAnimationFrame(frame);
      clearHighlights();
    };
  }, [needle, currentId]);

  const onScroll = () => {
    const el = scroller.current;
    if (!el) return;
    // A queued scroll event from our own follow-scroll may run after the next
    // reveal has grown the row. Only actual scroll movement changes follow mode.
    if (el.scrollTop !== lastScrollTop.current) {
      following.current = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
      lastScrollTop.current = el.scrollTop;
    }
    // Far from the live edge, offer the jump even when nothing new arrived.
    if (following.current) {
      if (showPill) setShowPill(false);
    } else if (!showPill && el.scrollHeight - el.scrollTop - el.clientHeight > el.clientHeight) {
      setShowPill(true);
    }
    if (el.scrollTop < NEAR_TOP_PX && snap.hasEarlier && !snap.loadingEarlier) void controller.loadEarlier();
  };

  const jump = () => {
    const el = scroller.current;
    if (!el) return;
    track.action("transcript.jump_latest", "click");
    following.current = true;
    el.scrollTop = el.scrollHeight;
    lastScrollTop.current = el.scrollTop;
    setShowPill(false);
  };

  const blocks = useMemo(() => transcriptBlocks(snap.rows, currentId), [snap.rows, currentId]);
  // A search match on a hidden step opens its block (and leaves it open).
  const revealKey = useMemo(() => {
    for (const b of blocks) if (b.type === "activity" && blockHides(snap.rows, b, currentId)) return b.key;
    return null;
  }, [blocks, snap.rows, currentId]);
  useEffect(() => {
    if (revealKey) setExpanded((prev) => (prev.has(revealKey) ? prev : new Set(prev).add(revealKey)));
  }, [revealKey]);
  const toggleBlock = (key: string) => {
    // Keep the reader where they clicked instead of chasing the live edge.
    following.current = false;
    quietGrowth.current = true;
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  };
  const renderRow = (i: number, repeat = 1) => {
    const row = snap.rows[i];
    return (
      <RowView
        key={row.id}
        row={row}
        controller={controller}
        loadingDetail={snap.loadingDetail.has(row.id)}
        report={row.kind.type === "report" ? reportPresentation(snap.rows, i) : undefined}
        repeat={repeat}
        search={
          search?.needle
            ? row.id === search.currentRowId
              ? "current"
              : search.matchIds.has(row.id)
                ? "match"
                : null
            : null
        }
      />
    );
  };
  // A flat, keyed list (no wrappers) so rows keep their state when a run
  // grows long enough to fold.
  const items: ReactNode[] = [];
  for (const b of blocks) {
    if (b.type === "row") {
      items.push(renderRow(b.index, b.repeat));
      continue;
    }
    const open = expanded.has(b.key) || b.key === revealKey;
    const tail = foldedTailStart(b);
    items.push(
      <ActivityHead
        key={`activity:${b.key}`}
        summary={activitySummary(snap.rows, b.start, b.end)}
        hidden={tail - b.start}
        open={open}
        onToggle={(via) => {
          track.action(open ? "transcript.fold_activity" : "transcript.expand_activity", via);
          toggleBlock(b.key);
        }}
      />,
    );
    for (let i = open ? b.start : tail; i < b.end; i++) items.push(renderRow(i));
  }

  const working =
    snap.mode === "live" &&
    snap.conn === "streaming" &&
    snap.phase.kind === "running" &&
    !snap.pauseRequested &&
    !snap.awaitsAnswer &&
    !snap.rows.some((r) => r.kind.type === "liveTail");

  return (
    <div className="transcript-wrap">
      <div className="transcript" ref={scroller} onScroll={onScroll}>
        <div className="transcript-inner" ref={inner}>
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
          {items}
          {snap.pendingMessages.map((m) => (
            <div key={m.id} className={`row turn user pending ${m.status}`}>
              <div className="turn-head">
                <span className="who">You</span>
                <span className={m.status === "failed" ? "tag error" : "tag"}>
                  {m.status === "sending" ? "sending…" : m.status === "sent" ? "sent" : "not sent"}
                </span>
              </div>
              {m.text && <div className="text">{m.text}</div>}
              {m.pictures.length > 0 && (
                <div className="pictures">
                  {m.pictures.map((p) =>
                    p.previewUrl ? (
                      <span key={p.id} className="picture-thumb static">
                        <img src={p.previewUrl} alt={p.filename} />
                      </span>
                    ) : (
                      <span key={p.id} className="tag picture-meta">
                        🖼 {p.filename}
                      </span>
                    ),
                  )}
                </div>
              )}
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
                      const draft = controller.discardSend(m.id);
                      if (draft) onEditFailed(draft.text, draft.pictures);
                    }}
                  >
                    Edit
                  </button>
                </div>
              )}
            </div>
          ))}
          {working && (
            <div className="working muted" role="status">
              <span className="spinner" aria-hidden="true" />
              Working…
            </div>
          )}
          {snap.mode === "live" && snap.awaitingJobs && (
            <div className="working muted" role="status">
              <span className="spinner" aria-hidden="true" />
              Waiting on background jobs…
            </div>
          )}
        </div>
      </div>
      {search && <SearchBar search={search} hasEarlier={snap.hasEarlier} />}
      {showPill && (
        <button type="button" className="pill" onClick={jump}>
          ↓ Jump to latest
        </button>
      )}
    </div>
  );
}

/** The summary line of a folded run of tool/reasoning steps. */
function ActivityHead({
  summary,
  hidden,
  open,
  onToggle,
}: {
  summary: ActivitySummary;
  hidden: number;
  open: boolean;
  onToggle: (via: ReturnType<typeof clickVia>) => void;
}) {
  const shown = summary.tools.slice(0, 4);
  const others = summary.tools.slice(4).reduce((n, t) => n + t.count, 0);
  const duration = compactDuration(summary.durationMs);
  return (
    <div className={`row fold activity-head${open ? " open" : ""}`}>
      <button
        type="button"
        className="activity-toggle"
        aria-expanded={open}
        title={open ? "Fold the earlier steps of this run" : `Show the ${hidden} earlier steps of this run`}
        onClick={(e) => onToggle(clickVia(e))}
      >
        <span className="sum-title">{summary.steps} steps</span>
        <span className="activity-counts">
          {shown.map((t) => (
            <span key={t.name} className="activity-count">
              {t.name} <b>{t.count}</b>
            </span>
          ))}
          {others > 0 && (
            <span className="activity-count">
              other <b>{others}</b>
            </span>
          )}
          {summary.reasoning > 0 && (
            <span className="activity-count">
              reasoning <b>{summary.reasoning}</b>
            </span>
          )}
        </span>
        <span className="sum-meta">
          {summary.failed > 0 && <span className="tag error">{summary.failed} failed</span>}
          {duration && <span className="activity-duration">{duration}</span>}
          <span className="activity-action">{open ? "Fold" : `Show ${hidden} more`}</span>
        </span>
      </button>
    </div>
  );
}

/** Scroll so the row sits in the upper third of the viewport. */
function scrollToRow(scroller: HTMLElement, rowId: string): boolean {
  const target = scroller.querySelector<HTMLElement>(`[data-row-id="${CSS.escape(rowId)}"]`);
  if (!target) return false;
  const top = target.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
  scroller.scrollTop = Math.max(0, top - scroller.clientHeight / 3);
  return true;
}

const MAX_HIGHLIGHT_RANGES = 2000;

interface HighlightRegistryLike {
  set(name: string, value: unknown): void;
  delete(name: string): void;
}

function highlightApi(): { registry: HighlightRegistryLike; make: (ranges: Range[]) => unknown } | null {
  const registry = (globalThis.CSS as unknown as { highlights?: HighlightRegistryLike } | undefined)?.highlights;
  const Ctor = (globalThis as unknown as { Highlight?: new (...ranges: Range[]) => unknown }).Highlight;
  if (!registry || !Ctor) return null;
  return { registry, make: (ranges) => new Ctor(...ranges) };
}

function clearHighlights() {
  const api = highlightApi();
  if (!api) return;
  api.registry.delete("ycc-search");
  api.registry.delete("ycc-search-current");
}

/** Highlight every occurrence of `needle` in the transcript's text nodes. */
function applyHighlights(root: HTMLElement, needle: string, currentRowId: string | null) {
  const api = highlightApi();
  if (!api) return;
  const all: Range[] = [];
  const current: Range[] = [];
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let count = 0;
  for (let node = walker.nextNode() as Text | null; node && count < MAX_HIGHLIGHT_RANGES; node = walker.nextNode() as Text | null) {
    const text = node.data.toLowerCase();
    if (text.length !== node.data.length) continue; // case folding changed offsets
    let i = text.indexOf(needle);
    if (i < 0) continue;
    const row = node.parentElement?.closest<HTMLElement>("[data-row-id]");
    const isCurrent = !!row && row.dataset.rowId === currentRowId;
    while (i >= 0 && count < MAX_HIGHLIGHT_RANGES) {
      const range = document.createRange();
      range.setStart(node, i);
      range.setEnd(node, i + needle.length);
      (isCurrent ? current : all).push(range);
      count++;
      i = text.indexOf(needle, i + needle.length);
    }
  }
  api.registry.set("ycc-search", api.make(all));
  api.registry.set("ycc-search-current", api.make(current));
}
