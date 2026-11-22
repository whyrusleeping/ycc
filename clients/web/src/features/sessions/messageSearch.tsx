// Full-text message search (SearchSessions) shared by the session list and the
// command palette: a debounced query that re-polls while the daemon is still
// indexing, and snippet rendering.
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client } from "../../api/client";
import { queryKeys } from "../../api/queries";
import type { SearchSessionsResponse, SessionSearchSpan } from "../../gen/ycc/v1/ycc_pb";
import { searchableQuery } from "./messageHits";

/** While a backfill runs, re-ask so hits fill in as sessions are indexed. */
const INDEXING_POLL_MS = 2_000;
const DEBOUNCE_MS = 250;

function useDebounced<T>(value: T, ms: number): T {
  const [out, setOut] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setOut(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return out;
}

export interface MessageSearch {
  /** The (debounced) query searched; "" when too short to send. */
  query: string;
  response: SearchSessionsResponse | undefined;
  /** A search for the current input has not answered yet. */
  pending: boolean;
  error: unknown;
}

export function useMessageSearch(project: string | null, input: string, limit: number): MessageSearch {
  const wanted = searchableQuery(input);
  const query = useDebounced(wanted, DEBOUNCE_MS);
  const settled = query === wanted;
  const result = useQuery<SearchSessionsResponse>({
    queryKey: queryKeys.sessionSearch(project ?? "", query, limit),
    enabled: project !== null && query !== "",
    // Keep the previous hits on screen while the next query is in flight, but
    // never another project's: their links would open in the wrong project.
    placeholderData: (prev, prevQuery) => (prevQuery?.queryKey[1] === (project ?? "") ? prev : undefined),
    staleTime: 10_000,
    refetchInterval: (q) => (q.state.data?.indexing ? INDEXING_POLL_MS : false),
    queryFn: ({ signal }) => client.searchSessions({ project: project ?? "", query, limit }, { signal }),
  });
  const active = project !== null && wanted !== "";
  return {
    query: active ? query : "",
    response: active ? result.data : undefined,
    pending: active && (!settled || result.isPlaceholderData || result.isPending),
    error: active ? result.error : null,
  };
}

/** A snippet with its matched terms highlighted. */
export function Snippet({ spans }: { spans: readonly SessionSearchSpan[] }) {
  return (
    <>
      {spans.map((s, i) => (s.match ? <mark key={i}>{s.text}</mark> : <span key={i}>{s.text}</span>))}
    </>
  );
}
