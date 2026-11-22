// Opening a session at one row (a session search hit): page earlier history
// in until the row is loaded, then hand the transcript a row to scroll to.
import { useEffect, useState } from "react";
import type { SessionController } from "./controller";
import { controllerSource } from "./SearchBar";
import { seekRow } from "./search";
import { toast } from "../../ui/toast";

export interface RowJumpTarget {
  rowId: string;
  /** Distinguishes navigations: opening the same hit again jumps again. */
  key: string;
}

export interface RowJump {
  /** The row to show and mark, once loaded. */
  rowId: string | null;
  /** Bumped per jump so the transcript scrolls even to the same row. */
  token: number;
  /** Earlier pages are loading to reach the row. */
  seeking: boolean;
}

export function useRowJump(controller: SessionController, installed: boolean, target: RowJumpTarget | null): RowJump {
  const [jump, setJump] = useState<RowJump>({ rowId: null, token: 0, seeking: false });
  const rowId = target?.rowId ?? null;
  const key = target?.key ?? "";
  useEffect(() => {
    if (!rowId) {
      setJump((j) => (j.rowId || j.seeking ? { ...j, rowId: null, seeking: false } : j));
      return;
    }
    if (!installed) return;
    let cancelled = false;
    const source = controllerSource(controller);
    if (!source.rows().some((r) => r.id === rowId)) setJump((j) => ({ ...j, rowId: null, seeking: true }));
    void seekRow(source, rowId, () => cancelled).then((result) => {
      if (cancelled || result === "cancelled") return;
      if (result === "found") {
        setJump((j) => ({ rowId, token: j.token + 1, seeking: false }));
        return;
      }
      setJump((j) => ({ ...j, rowId: null, seeking: false }));
      // A failed page already raised its own error toast.
      if (result === "missing") toast("The matching message is no longer in this session.", "info");
    });
    return () => {
      cancelled = true;
    };
  }, [controller, installed, rowId, key]);
  return jump;
}
