// Transcript search state and the find bar (Ctrl/Cmd-F within a session).
// Typing searches the loaded rows (newest match first) without paging; Enter
// (older) and Shift+Enter (newer) step through matches, paging earlier
// history in until an older match turns up.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { SessionController } from "./controller";
import type { TranscriptRow } from "./projection";
import { findMatch, matchingRowIds, normalizeQuery, type SearchDirection, type SearchSource } from "./search";

export type SearchStatus = "idle" | "searching" | "found" | "wrapped" | "end" | "none" | "noneLoaded";

export interface TranscriptSearch {
  open: boolean;
  query: string;
  needle: string;
  currentRowId: string | null;
  /** Bumped on every jump so the transcript re-scrolls even to the same row. */
  token: number;
  status: SearchStatus;
  matchIds: ReadonlySet<string>;
  /** 1-based position of the current match among loaded matches (0: none). */
  position: number;
  show: () => void;
  close: () => void;
  setQuery: (q: string) => void;
  step: (dir: SearchDirection) => void;
  focusToken: number;
}

function controllerSource(controller: SessionController): SearchSource {
  return {
    rows: () => controller.projection.rows,
    hasEarlier: () => controller.hasEarlier(),
    loadEarlier: () => controller.loadEarlier(),
  };
}

export function useTranscriptSearch(controller: SessionController, rows: readonly TranscriptRow[]): TranscriptSearch {
  const [open, setOpen] = useState(false);
  const [query, setQueryState] = useState("");
  const [currentRowId, setCurrent] = useState<string | null>(null);
  const [token, setToken] = useState(0);
  const [status, setStatus] = useState<SearchStatus>("idle");
  const [focusToken, setFocusToken] = useState(0);
  const generation = useRef(0);
  const queryRef = useRef("");
  const currentRef = useRef<string | null>(null);
  currentRef.current = currentRowId;

  useEffect(() => () => {
    generation.current++;
  }, []);
  // A different session starts a fresh search.
  useEffect(() => {
    generation.current++;
    setOpen(false);
    setQueryState("");
    queryRef.current = "";
    setCurrent(null);
    setStatus("idle");
  }, [controller]);

  const needle = open ? normalizeQuery(query) : "";
  const matchList = useMemo(() => (needle ? matchingRowIds(rows, needle) : []), [rows, needle]);
  const matchIds = useMemo(() => new Set(matchList), [matchList]);
  const position = currentRowId ? matchList.indexOf(currentRowId) + 1 : 0;

  const run = useCallback(
    async (q: string, from: string | null, dir: SearchDirection, page: boolean) => {
      const gen = ++generation.current;
      const cancelled = () => gen !== generation.current;
      const base = controllerSource(controller);
      const source: SearchSource = page
        ? base
        : { rows: base.rows, hasEarlier: () => false, loadEarlier: async () => false };
      if (page && base.hasEarlier()) setStatus("searching");
      const result = await findMatch(source, q, from, dir, cancelled);
      if (cancelled()) return;
      switch (result.kind) {
        case "found":
          setCurrent(result.rowId);
          setToken((t) => t + 1);
          setStatus(result.wrapped ? "wrapped" : "found");
          break;
        case "end":
          setStatus("end");
          break;
        case "none":
          if (from === null) setCurrent(null);
          setStatus(!page && controller.hasEarlier() ? "noneLoaded" : "none");
          break;
        case "cancelled":
          break;
      }
    },
    [controller],
  );

  // Incremental: loaded rows only, newest match first, debounced.
  useEffect(() => {
    if (!open) return;
    const q = normalizeQuery(query);
    if (!q) {
      generation.current++;
      setCurrent(null);
      setStatus("idle");
      return;
    }
    const t = setTimeout(() => void run(q, null, "older", false), 150);
    return () => clearTimeout(t);
  }, [open, query, run]);

  const step = useCallback(
    (dir: SearchDirection) => {
      const q = normalizeQuery(queryRef.current);
      if (!q) return;
      void run(q, currentRef.current, dir, true);
    },
    [run],
  );

  const show = useCallback(() => {
    setOpen(true);
    setFocusToken((t) => t + 1);
  }, []);
  const close = useCallback(() => {
    generation.current++;
    setOpen(false);
    setCurrent(null);
    setStatus("idle");
  }, []);
  const setQuery = useCallback((q: string) => {
    queryRef.current = q;
    setQueryState(q);
  }, []);

  return {
    open,
    query,
    needle,
    currentRowId: open ? currentRowId : null,
    token,
    status,
    matchIds,
    position,
    focusToken,
    show,
    close,
    setQuery,
    step,
  };
}

export function SearchBar({ search, hasEarlier }: { search: TranscriptSearch; hasEarlier: boolean }) {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (search.open) {
      input.current?.focus();
      input.current?.select();
    }
  }, [search.open, search.focusToken]);
  if (!search.open) return null;
  const total = search.matchIds.size;
  let note = "";
  switch (search.status) {
    case "searching":
      note = "Searching earlier history…";
      break;
    case "none":
      note = "No matches";
      break;
    case "noneLoaded":
      note = "No loaded matches — Enter searches earlier history";
      break;
    case "end":
      note = "Newest match";
      break;
    case "wrapped":
      note = "Wrapped around";
      break;
  }
  const counter = search.needle ? `${search.position || "–"}/${total}${hasEarlier ? "+" : ""}` : "";
  return (
    <div className="search-bar" role="search">
      <input
        ref={input}
        type="search"
        className="field"
        aria-label="Search transcript"
        placeholder="Search this session"
        value={search.query}
        onChange={(e) => search.setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            search.step(e.shiftKey ? "newer" : "older");
          } else if (e.key === "Escape") {
            e.preventDefault();
            search.close();
          }
        }}
      />
      <span className="search-count" aria-live="polite" title={hasEarlier ? "+ earlier history is not loaded yet" : undefined}>
        {counter}
      </span>
      <button
        type="button"
        className="btn ghost small"
        title="Older match (Enter)"
        aria-label="Older match"
        disabled={!search.needle}
        onClick={() => search.step("older")}
      >
        ↑
      </button>
      <button
        type="button"
        className="btn ghost small"
        title="Newer match (Shift+Enter)"
        aria-label="Newer match"
        disabled={!search.needle}
        onClick={() => search.step("newer")}
      >
        ↓
      </button>
      <button type="button" className="btn ghost small" title="Close (Esc)" aria-label="Close search" onClick={search.close}>
        ×
      </button>
      {note && <span className={`search-note${search.status === "searching" ? " busy" : ""}`}>{note}</span>}
    </div>
  );
}
