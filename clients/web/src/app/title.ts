// The tab title: the routed surface's title, prefixed with how many sessions
// are waiting for an answer, e.g. "(2) Backlog · alpha · ycc".
import { useEffect } from "react";
import { titleWithCount } from "../features/notify/policy";

let base = "ycc";
let needsAnswer = 0;

function apply() {
  if (typeof document !== "undefined") document.title = titleWithCount(base, needsAnswer);
}

export function setNeedsAnswerCount(n: number) {
  if (n === needsAnswer) return;
  needsAnswer = n;
  apply();
}

/** Title the tab after the calling surface while it is mounted. */
export function useDocumentTitle(title: string) {
  useEffect(() => {
    base = `${title} · ycc`;
    apply();
    return () => {
      base = "ycc";
      apply();
    };
  }, [title]);
}
