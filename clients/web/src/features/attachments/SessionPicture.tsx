// Transcript pictures: thumbnails of a user turn's attachments fetched with
// GetSessionAttachment, opening full-size in the inspector. Event logs keep
// only picture metadata, so when the retained bytes are unavailable (older
// sessions, pruned or oversized files) the row falls back to that metadata.
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { client, errorMessage } from "../../api/client";
import type { Picture } from "../session/projection";
import { formatBytes, isPictureType } from "./attachments";

export interface SessionRef {
  project: string;
  sessionId: string;
}

export function useSessionAttachment(session: SessionRef, attachmentId: string) {
  return useQuery({
    queryKey: ["sessionAttachment", session.project, session.sessionId, attachmentId],
    enabled: attachmentId !== "",
    staleTime: Infinity,
    gcTime: 10 * 60_000,
    refetchOnWindowFocus: false,
    retry: (count, err) => {
      const code = ConnectError.from(err).code;
      return code !== Code.NotFound && code !== Code.InvalidArgument && code !== Code.Unauthenticated && count < 1;
    },
    queryFn: async ({ signal }) => {
      const r = await client.getSessionAttachment(
        { project: session.project, sessionId: session.sessionId, attachmentId },
        { signal },
      );
      if (!r.data.length) throw new Error("The picture’s bytes are not available.");
      return { data: r.data, mediaType: r.mediaType };
    },
  });
}

/** An object URL for bytes, revoked when they change or the caller unmounts. */
export function useObjectUrl(data: Uint8Array | undefined, type: string | undefined): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!data || !type || !isPictureType(type)) {
      setUrl(null);
      return;
    }
    const u = URL.createObjectURL(new Blob([data as BlobPart], { type }));
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [data, type]);
  return url;
}

function label(p: Picture): string {
  return p.filename || p.mediaType || "picture";
}

/** One transcript thumbnail; clicking opens it in the inspector. */
export function PictureThumb({
  session,
  picture,
  onOpen,
}: {
  session: SessionRef;
  picture: Picture;
  onOpen?: () => void;
}) {
  const q = useSessionAttachment(session, picture.attachmentId);
  const url = useObjectUrl(q.data?.data, q.data?.mediaType);
  const [broken, setBroken] = useState(false);
  if (!picture.attachmentId || q.isError || broken || (q.data && !isPictureType(q.data.mediaType))) {
    const why = !picture.attachmentId
      ? "not retained"
      : broken
        ? "can’t be displayed"
        : errorMessage(q.error, "unavailable");
    return (
      <span className="tag picture-meta" title={`${label(picture)} — ${why}`}>
        🖼 {label(picture)}
        <span className="muted"> · unavailable</span>
      </span>
    );
  }
  return (
    <button
      type="button"
      className="picture-thumb"
      title={`${label(picture)} — open in the inspector`}
      onClick={onOpen}
      disabled={!onOpen}
    >
      {url ? (
        <img src={url} alt={label(picture)} onError={() => setBroken(true)} />
      ) : (
        <span className="picture-loading muted">Loading…</span>
      )}
    </button>
  );
}

/** Full-size picture for the inspector. */
export function PictureDetail({ session, picture }: { session: SessionRef; picture: Picture }) {
  const q = useSessionAttachment(session, picture.attachmentId);
  const url = useObjectUrl(q.data?.data, q.data?.mediaType);
  const [broken, setBroken] = useState(false);
  const meta = (
    <div className="diff-meta">
      <span>{label(picture)}</span>
      {(q.data?.mediaType || picture.mediaType) && <span>{q.data?.mediaType || picture.mediaType}</span>}
      {q.data && <span>{formatBytes(q.data.data.length)}</span>}
    </div>
  );
  if (!picture.attachmentId) {
    return (
      <div>
        {meta}
        <p className="muted">This picture’s bytes were not retained; only its metadata is in the session log.</p>
      </div>
    );
  }
  if (q.isPending) return <p className="muted">Loading…</p>;
  if (q.data && !isPictureType(q.data.mediaType)) {
    return (
      <div>
        {meta}
        <p className="muted">Unsupported picture type.</p>
      </div>
    );
  }
  if (q.isError || broken) {
    return (
      <div>
        {meta}
        <p className="error">{broken ? "The picture can’t be displayed." : errorMessage(q.error, "The picture is unavailable.")}</p>
      </div>
    );
  }
  return (
    <div className="picture-detail">
      {meta}
      {url && <img src={url} alt={label(picture)} onError={() => setBroken(true)} />}
    </div>
  );
}
