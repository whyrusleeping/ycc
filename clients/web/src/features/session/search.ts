// In-session transcript search: case-insensitive substring matching over each
// row's full textual content, and a stepper that walks matches older/newer and
// pages earlier history in as needed (a match may live in a page that is not
// loaded yet). Pure apart from the injected source, so it is unit-tested.
import type { TranscriptRow } from "./projection";

/** Everything searchable in a row, as plain text. */
export function rowSearchText(row: TranscriptRow): string {
  const k = row.kind;
  switch (k.type) {
    case "user":
      return [k.text, ...k.pictures.map((p) => p.filename)].join("\n");
    case "model":
    case "report":
    case "thinking":
    case "liveTail":
    case "system":
    case "commit":
      return k.text;
    case "tool":
      return [k.name, k.args, k.output].join("\n");
    case "question":
      return [k.prompt, ...k.options, k.answer ?? ""].join("\n");
    case "assumption":
      return [...k.questions.flatMap((q) => [q.prompt, ...q.options]), k.response ?? ""].join("\n");
    case "review":
      return [k.text, k.summary].join("\n");
  }
}

export function normalizeQuery(query: string): string {
  return query.trim().toLowerCase();
}

export function rowMatches(row: TranscriptRow, needle: string): boolean {
  return needle !== "" && rowSearchText(row).toLowerCase().includes(needle);
}

/** Ids of matching rows in transcript order (oldest first). */
export function matchingRowIds(rows: readonly TranscriptRow[], query: string): string[] {
  const needle = normalizeQuery(query);
  if (!needle) return [];
  return rows.filter((r) => rowMatches(r, needle)).map((r) => r.id);
}

export interface SearchSource {
  rows(): readonly TranscriptRow[];
  hasEarlier(): boolean;
  /** Load the next earlier page; resolves false when nothing more loaded. */
  loadEarlier(): Promise<boolean>;
}

export type SearchDirection = "older" | "newer";

export type SearchResult =
  | { kind: "found"; rowId: string; wrapped: boolean }
  /** No match anywhere in the (fully loaded) history. */
  | { kind: "none" }
  /** Newer than the newest match: stays put (history above isn't loaded). */
  | { kind: "end"; rowId: string }
  | { kind: "cancelled" };

/**
 * Find the next match from `fromRowId` in `direction`. With no current match
 * (`fromRowId` null or no longer loaded) it finds the newest match, paging
 * earlier history until one appears. Moving older past the oldest loaded
 * match pages earlier history; once the whole history is loaded it wraps.
 * `isCancelled` is polled between pages (the query changed, the view closed).
 */
export async function findMatch(
  source: SearchSource,
  query: string,
  fromRowId: string | null,
  direction: SearchDirection,
  isCancelled: () => boolean = () => false,
): Promise<SearchResult> {
  const needle = normalizeQuery(query);
  if (!needle) return { kind: "none" };
  for (;;) {
    if (isCancelled()) return { kind: "cancelled" };
    const rows = source.rows();
    const from = fromRowId === null ? -1 : rows.findIndex((r) => r.id === fromRowId);
    if (from < 0) {
      for (let i = rows.length - 1; i >= 0; i--) {
        if (rowMatches(rows[i], needle)) return { kind: "found", rowId: rows[i].id, wrapped: false };
      }
    } else if (direction === "older") {
      for (let i = from - 1; i >= 0; i--) {
        if (rowMatches(rows[i], needle)) return { kind: "found", rowId: rows[i].id, wrapped: false };
      }
    } else {
      for (let i = from + 1; i < rows.length; i++) {
        if (rowMatches(rows[i], needle)) return { kind: "found", rowId: rows[i].id, wrapped: false };
      }
      if (source.hasEarlier()) return { kind: "end", rowId: fromRowId! };
      // Whole history loaded: wrap to the oldest match.
      for (let i = 0; i <= from; i++) {
        if (rowMatches(rows[i], needle)) return { kind: "found", rowId: rows[i].id, wrapped: true };
      }
      return { kind: "none" };
    }
    if (source.hasEarlier()) {
      const loaded = await source.loadEarlier();
      if (!loaded && !source.hasEarlier()) continue; // re-scan once: history is now complete
      if (!loaded) return isCancelled() ? { kind: "cancelled" } : { kind: "none" };
      continue;
    }
    // Everything is loaded and nothing older matched.
    if (from >= 0 && direction === "older") {
      for (let i = rows.length - 1; i >= from; i--) {
        if (rowMatches(rows[i], needle)) return { kind: "found", rowId: rows[i].id, wrapped: true };
      }
    }
    return { kind: "none" };
  }
}
