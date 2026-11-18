// Publishing a viewed file to the configured share server
// (docs/design/publishing.md). Nothing renders without a [publish] target.
// While the file's present contents have no live link the control offers
// "Publish to <name>"; once they do, a "Published" popover holds the link,
// Copy link, Open, its expiry and Unpublish. Links to earlier versions are
// listed in the same popover, reachable beside the publish button when the
// present contents are unpublished.
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useRef, useState } from "react";
import { client, errorMessage } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { track } from "../../app/analytics";
import type { Publication } from "../../gen/ycc/v1/ycc_pb";
import { ConfirmDialog } from "../../ui/ConfirmDialog";
import { CopyButton } from "../../ui/CopyButton";
import { copyText } from "../../ui/copy";
import { toast } from "../../ui/toast";
import { relativeTime } from "../sessions/feed";
import { expiryLabel } from "./model";
import type { FileTarget } from "./preview";

export function PublishControl({ target, compact }: { target: FileTarget; compact: boolean }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: queryKeys.publications(target.project, target.sessionId, target.path),
    queryFn: ({ signal }) =>
      client.getPublications({ project: target.project, sessionId: target.sessionId, path: target.path }, { signal }),
  });
  const [busy, setBusy] = useState<"publish" | "unpublish" | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState<Publication | null>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLDivElement>(null);
  const popId = useId();

  useEffect(() => {
    setError(null);
    setOpen(false);
  }, [target.project, target.sessionId, target.path]);
  useEffect(() => {
    if (!open) return;
    pop.current?.querySelector<HTMLElement>("button, a")?.focus();
    const onDown = (e: MouseEvent) => {
      if (wrap.current && !wrap.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  // No [publish] config (or a daemon without publishing): show nothing.
  const share = q.data?.target;
  if (!q.data || !share) return null;
  const current = q.data.publications.find((p) => p.current) ?? null;
  const earlier = q.data.publications.filter((p) => !p.current);

  const refresh = () => qc.invalidateQueries({ queryKey: queryKeys.publicationsAll });

  const publish = async () => {
    setBusy("publish");
    setError(null);
    try {
      const r = await client.publishFile({ project: target.project, sessionId: target.sessionId, path: target.path });
      track.action("files.publish", "click", { reused: r.reused });
      const url = r.publication?.url ?? "";
      const copied = url !== "" && (await copyText(url));
      toast(`${r.reused ? "Already published" : "Published"}${copied ? " — link copied" : ""}: ${url}`, "info");
      await refresh();
      setOpen(true);
    } catch (err) {
      track.error("files.publish", err);
      setError(errorMessage(err, "Couldn’t publish."));
    } finally {
      setBusy(null);
    }
  };

  const unpublish = async (p: Publication) => {
    setConfirming(null);
    setBusy("unpublish");
    setError(null);
    try {
      await client.unpublishFile({ project: target.project, id: p.id });
      toast("Unpublished: the link no longer works.", "info");
    } catch (err) {
      track.error("files.unpublish", err);
      setError(errorMessage(err, "Couldn’t unpublish."));
    } finally {
      setBusy(null);
      await refresh();
    }
  };

  const closePop = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  const askUnpublish = (p: Publication) => {
    setOpen(false);
    setConfirming(p);
  };

  return (
    <div
      className="publish-control"
      ref={wrap}
      onBlur={(e) => {
        const to = e.relatedTarget as Node | null;
        if (to && !e.currentTarget.contains(to)) setOpen(false);
      }}
      onKeyDown={(e) => {
        if (open && e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          closePop();
        }
      }}
    >
      {!current && (
        <button
          type="button"
          className="btn ghost small"
          disabled={busy !== null}
          title={`Publish this file to ${share.name} as a secret public link`}
          aria-label={`Publish to ${share.name}`}
          onClick={() => void publish()}
        >
          {busy === "publish" ? "Publishing…" : compact ? "Publish" : `Publish to ${share.name}`}
        </button>
      )}
      {(current || earlier.length > 0) && (
        <button
          ref={trigger}
          type="button"
          className={`btn ghost small${open ? " active" : ""}`}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-controls={open ? popId : undefined}
          title={current ? `Published to ${share.name}: show the link` : "Links to earlier versions of this file"}
          disabled={busy === "unpublish"}
          onClick={() => (open ? closePop() : setOpen(true))}
        >
          {busy === "unpublish" ? (
            "Unpublishing…"
          ) : current ? (
            <>
              <span className="publish-mark" aria-hidden />
              Published
            </>
          ) : (
            `Earlier (${earlier.length})`
          )}
        </button>
      )}
      {error && (
        <span className="error small publish-error" role="alert" title={error}>
          {error}
        </span>
      )}
      {open && (
        <div ref={pop} id={popId} className="publish-pop" role="dialog" aria-label="Published links">
          {current && (
            <PublicationRow
              heading={`Published to ${share.name}`}
              p={current}
              current
              onUnpublish={() => askUnpublish(current)}
            />
          )}
          {earlier.map((p) => (
            <PublicationRow key={p.id} heading={`Earlier version · ${relativeTime(p.publishedAt)}`} p={p} onUnpublish={() => askUnpublish(p)} />
          ))}
        </div>
      )}
      <ConfirmDialog
        open={confirming !== null}
        title="Unpublish this link?"
        body={`The ${confirming?.current ? "" : "earlier version’s "}link stops working for anyone who has it. The file itself is not changed.`}
        confirmLabel="Unpublish"
        danger
        action="files.unpublish"
        onConfirm={() => confirming && void unpublish(confirming)}
        onCancel={() => setConfirming(null)}
      />
    </div>
  );
}

function PublicationRow({
  heading,
  p,
  current = false,
  onUnpublish,
}: {
  heading: string;
  p: Publication;
  current?: boolean;
  onUnpublish: () => void;
}) {
  return (
    <section className={`publish-entry${current ? " current" : ""}`}>
      <div className="publish-head">
        {current && <span className="publish-mark" aria-hidden />}
        {heading}
      </div>
      <a className="publish-url mono" href={p.url} target="_blank" rel="noreferrer" data-track="files.publication_open">
        {p.url}
      </a>
      <div className="publish-meta muted">
        {expiryLabel(p.expiresAt)}
        {current && p.publishedAt && <> · published {relativeTime(p.publishedAt)}</>}
      </div>
      <div className="publish-actions">
        <CopyButton text={p.url} label="Copy link" title="Copy the public link" what="publication_link" />
        {current && (
          <a className="copy-btn" href={p.url} target="_blank" rel="noreferrer" data-track="files.publication_open">
            Open
          </a>
        )}
        <button type="button" className="copy-btn danger-text" onClick={onUnpublish}>
          Unpublish
        </button>
      </div>
    </section>
  );
}
