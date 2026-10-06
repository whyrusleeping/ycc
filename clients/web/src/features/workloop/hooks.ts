// Shell-level work-loop observation: the union of loop-owned session ids (for
// "loop" markers in session lists), finish announcements when a watched loop
// ends, and a session-list refresh when a loop moves to its next session.
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef } from "react";
import type { WorkLoopInfo } from "../../gen/ycc/v1/ycc_pb";
import { queryKeys, useWorkLoops } from "../../api/queries";
import { toast } from "../../ui/toast";
import { currentSessionId, finishAnnouncement, loopSessionIds } from "./model";

/** Session ids any project's loop ran or is running. */
export function useLoopSessionIds(): Set<string> {
  const loops = useWorkLoops();
  const key = loops.map((l) => `${l.loop?.loopId ?? ""}:${l.loop?.sessions.length ?? 0}:${currentSessionId(l.loop)}`).join("|");
  // Recomputed only when some loop's session set changes (the key), not per render.
  return useMemo(() => {
    const ids = new Set<string>();
    for (const l of loops) for (const id of loopSessionIds(l.loop)) ids.add(id);
    return ids;
  }, [key]);
}

/** Announce finished loops and keep the session lists in step with loops (mounted once by the shell). */
export function useLoopWatcher() {
  const loops = useWorkLoops();
  const qc = useQueryClient();
  const seen = useRef(new Map<string, WorkLoopInfo | null>());
  useEffect(() => {
    for (const { project, loop } of loops) {
      const had = seen.current.has(project);
      const prev = seen.current.get(project) ?? null;
      if (had && prev === loop) continue;
      seen.current.set(project, loop);
      if (!had) continue;
      const note = finishAnnouncement(prev, loop);
      if (note) toast(note.text, note.failure ? "error" : "info");
      // A new loop session (or a finished one) changes the session lists.
      if (currentSessionId(prev) !== currentSessionId(loop) || (prev?.sessions.length ?? 0) !== (loop?.sessions.length ?? 0)) {
        void qc.invalidateQueries({ queryKey: queryKeys.sessionFeedAll });
      }
    }
  }, [loops, qc]);
}
