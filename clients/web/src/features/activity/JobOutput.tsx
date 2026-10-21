// A background shell job's output: the retained tail first, then followed by
// byte cursor about once a second while the job runs. Falls back to the
// node's bounded result when the daemon cannot serve the output.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { client, errorMessage } from "../../api/client";
import { JobOutputBuffer } from "./model";

const TAIL_LINES = 500;
const CHUNK_BYTES = 256 * 1024;
const FOLLOW_MS = 1000;

interface OutputView {
  loaded: boolean;
  text: string;
  status: string;
  /** Bytes the daemon had already dropped before the first byte shown. */
  droppedBefore: bigint;
  /** Retained bytes not shown because only the tail was loaded. */
  tailOnly: boolean;
  droppedWhileFollowing: bigint;
  trimmed: boolean;
  unavailable: string | null;
  error: string | null;
}

const EMPTY: OutputView = {
  loaded: false,
  text: "",
  status: "",
  droppedBefore: 0n,
  tailOnly: false,
  droppedWhileFollowing: 0n,
  trimmed: false,
  unavailable: null,
  error: null,
};

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const onAbort = () => {
      clearTimeout(t);
      resolve();
    };
    const t = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

function useJobOutput(project: string, sessionId: string, jobId: string, enabled: boolean): OutputView {
  const [view, setView] = useState<OutputView>(EMPTY);
  useEffect(() => {
    setView(EMPTY);
    if (!enabled) return;
    const ctl = new AbortController();
    const buf = new JobOutputBuffer();
    void (async () => {
      let first = true;
      while (!ctl.signal.aborted) {
        try {
          const r = await client.getJobOutput(
            first
              ? { project, sessionId, jobId, tailLines: TAIL_LINES, limit: CHUNK_BYTES }
              : { project, sessionId, jobId, offset: buf.cursor, limit: CHUNK_BYTES },
            { signal: ctl.signal },
          );
          if (ctl.signal.aborted) return;
          if (!r.available) {
            buf.finish();
            setView((v) => ({
              ...v,
              loaded: true,
              text: buf.text,
              status: "",
              unavailable: r.unavailableReason || "Output is not available.",
              error: null,
            }));
            return;
          }
          first = false;
          buf.apply(r);
          const done = r.status !== "running" && r.end >= r.retainedEnd;
          if (done) buf.finish();
          setView({
            loaded: true,
            text: buf.text,
            status: r.status,
            droppedBefore: buf.droppedBefore,
            tailOnly: (buf.firstStart ?? 0n) > buf.droppedBefore,
            droppedWhileFollowing: buf.droppedWhileFollowing,
            trimmed: buf.trimmed,
            unavailable: null,
            error: null,
          });
          if (done) return;
          if (r.end < r.retainedEnd) continue; // still catching up
        } catch (err) {
          if (ctl.signal.aborted) return;
          setView((v) => ({ ...v, loaded: true, error: errorMessage(err) }));
        }
        await sleep(FOLLOW_MS, ctl.signal);
      }
    })();
    return () => ctl.abort();
  }, [project, sessionId, jobId, enabled]);
  return view;
}

function bytes(n: bigint): string {
  const v = Number(n);
  if (v < 1024) return `${v} B`;
  if (v < 1024 * 1024) return `${(v / 1024).toFixed(1)} KiB`;
  return `${(v / 1024 / 1024).toFixed(1)} MiB`;
}

export function JobOutput({
  project,
  sessionId,
  jobId,
  available,
  result,
}: {
  project: string;
  sessionId: string;
  jobId: string;
  /** The activity node says GetJobOutput can read this job. */
  available: boolean;
  /** The node's bounded result: the fallback when output is unavailable. */
  result: string;
}) {
  const view = useJobOutput(project, sessionId, jobId, available);
  const pre = useRef<HTMLPreElement>(null);
  const following = useRef(true);
  useLayoutEffect(() => {
    const el = pre.current;
    if (el && following.current) el.scrollTop = el.scrollHeight;
  }, [view.text]);

  if (!available || view.unavailable) {
    // The daemon cannot serve (any more of) the output: the node's final
    // result is authoritative; output read before then is only a partial view.
    const partial = available ? view.text : "";
    return (
      <div className="job-output">
        <p className="notice muted">
          {view.unavailable ?? "Live output is not available: it lives only in the running daemon's session."}
          {result ? " Showing the job's final result." : ""}
        </p>
        {result ? (
          <pre className="job-output-text mono">{result}</pre>
        ) : (
          !partial && <p className="muted small">No result was recorded.</p>
        )}
        {partial && (
          <>
            <p className="notice warn">
              {result ? "Output read before it became unavailable (partial):" : "No result was recorded. Output read before it became unavailable (partial):"}
            </p>
            <pre className="job-output-text mono">{partial}</pre>
          </>
        )}
      </div>
    );
  }
  if (!view.loaded) return <p className="muted">Loading output…</p>;
  const notices: string[] = [];
  if (view.droppedBefore > 0n) notices.push(`The first ${bytes(view.droppedBefore)} of output were dropped by the daemon's bounded buffer.`);
  if (view.tailOnly) notices.push(`Showing the last ${TAIL_LINES} lines.`);
  if (view.droppedWhileFollowing > 0n) notices.push(`${bytes(view.droppedWhileFollowing)} were dropped before they could be read.`);
  if (view.trimmed) notices.push("The oldest output shown here was trimmed.");
  return (
    <div className="job-output">
      {notices.length > 0 && <p className="notice warn">{notices.join(" ")}</p>}
      {view.error && <p className="notice error">{view.error}</p>}
      <pre
        ref={pre}
        className="job-output-text mono"
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {view.text || <span className="muted">(no output yet)</span>}
        {view.status === "running" && <span className="job-output-cursor" aria-hidden="true" />}
      </pre>
    </div>
  );
}
