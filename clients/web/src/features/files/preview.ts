// ReadFile for previews, shared by the file viewer and markdown images so a
// file read for one is cached for the other.
import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { PDF_TYPE, VIEWER_MAX_BYTES, isRasterPreviewType } from "./model";

export interface FileTarget {
  project: string;
  /** Resolve against this session's live worktree ("" = the project root). */
  sessionId: string;
  path: string;
}

export function useFileRead(t: FileTarget, enabled = true) {
  return useQuery({
    queryKey: queryKeys.fileRead(t.project, t.sessionId, t.path),
    enabled,
    queryFn: ({ signal }) =>
      client.readFile(
        { project: t.project, sessionId: t.sessionId, path: t.path, maxBytes: BigInt(VIEWER_MAX_BYTES) },
        { signal },
      ),
  });
}

/** A blob: URL for previewable bytes (raster images, PDF), revoked on change. */
export function useBlobUrl(data: Uint8Array | undefined, mediaType: string | undefined): string | null {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    if (!data?.length || !mediaType || !(isRasterPreviewType(mediaType) || mediaType === PDF_TYPE)) {
      setUrl(null);
      return;
    }
    const u = URL.createObjectURL(new Blob([data as BlobPart], { type: mediaType }));
    setUrl(u);
    return () => URL.revokeObjectURL(u);
  }, [data, mediaType]);
  return url;
}
