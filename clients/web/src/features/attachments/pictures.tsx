// Browser plumbing for composer pictures: reading (and, when oversized,
// downscaling) files, the thumbnail strip, the attach button, and paste/drop
// handlers. The rules themselves live in attachments.ts (unit-tested).
import { useCallback, useEffect, useRef, useState, type DragEvent } from "react";
import {
  DOWNSCALE_MAX_EDGE,
  MAX_PICTURES,
  MAX_PICTURE_BYTES,
  PICTURE_ACCEPT,
  declaredType,
  fitWithin,
  formatBytes,
  needsDownscale,
  pictureFilename,
  planAdd,
  type DraftPicture,
} from "./attachments";

let nextId = 1;

function objectUrl(bytes: Uint8Array, type: string): string | null {
  try {
    return URL.createObjectURL(new Blob([bytes as BlobPart], { type }));
  } catch {
    return null;
  }
}

/** Re-encode an oversized still picture as a bounded JPEG; null if impossible. */
async function downscale(file: Blob): Promise<Uint8Array | null> {
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(file);
  } catch {
    return null;
  }
  try {
    const { width, height } = fitWithin(bitmap.width, bitmap.height, DOWNSCALE_MAX_EDGE);
    const canvas = document.createElement("canvas");
    canvas.width = width;
    canvas.height = height;
    const ctx = canvas.getContext("2d");
    if (!ctx) return null;
    // JPEG has no alpha: flatten transparent screenshots onto white.
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, width, height);
    ctx.drawImage(bitmap, 0, 0, width, height);
    for (const quality of [0.85, 0.7, 0.55, 0.4]) {
      const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, "image/jpeg", quality));
      if (blob && blob.size <= MAX_PICTURE_BYTES) return new Uint8Array(await blob.arrayBuffer());
    }
    return null;
  } finally {
    bitmap.close();
  }
}

/**
 * Stage files as draft pictures given `current` staged ones. Non-pictures and
 * overflow are refused with a message; oversized stills are downscaled. What
 * cannot be fixed client-side is staged unchanged for the daemon to judge.
 */
export async function loadPictures(
  files: File[],
  current: number,
): Promise<{ pictures: DraftPicture[]; errors: string[] }> {
  const plan = planAdd(
    current,
    files.map((f) => ({ name: f.name, type: f.type, size: f.size })),
  );
  const pictures: DraftPicture[] = [];
  const errors = [...plan.errors];
  for (const index of plan.accept) {
    const file = files[index];
    let data: Uint8Array;
    try {
      data = new Uint8Array(await file.arrayBuffer());
    } catch {
      errors.push(`Couldn’t read ${file.name || "a picture"}.`);
      continue;
    }
    let mediaType = declaredType(data, file.type);
    let note: string | undefined;
    if (needsDownscale({ type: mediaType, size: data.length })) {
      const smaller = await downscale(file);
      if (smaller) {
        note = `Downscaled from ${formatBytes(data.length)}`;
        data = smaller;
        mediaType = "image/jpeg";
      }
    }
    pictures.push({
      id: `pic-${nextId++}`,
      data,
      mediaType,
      filename: pictureFilename(file.name, current + pictures.length, mediaType),
      previewUrl: objectUrl(data, mediaType),
      note,
    });
  }
  return { pictures, errors };
}

export function revokePictures(pictures: readonly DraftPicture[]) {
  for (const p of pictures) if (p.previewUrl) URL.revokeObjectURL(p.previewUrl);
}

/** Image files on a paste/drop data transfer (non-files are ignored). */
export function filesFrom(data: DataTransfer | null): File[] {
  if (!data) return [];
  const out: File[] = [];
  if (data.items && data.items.length) {
    for (const item of Array.from(data.items)) {
      if (item.kind !== "file") continue;
      const f = item.getAsFile();
      if (f) out.push(f);
    }
    return out;
  }
  return Array.from(data.files ?? []);
}

/**
 * A staged-picture draft: add (with validation messages), remove, clear.
 * `initial`/`onChange` let a caller persist the draft (per-session composer
 * drafts) across remounts.
 */
export function usePictureDraft(initial: DraftPicture[] = [], onChange?: (pictures: DraftPicture[]) => void) {
  const [pictures, setPictures] = useState<DraftPicture[]>(initial);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const latest = useRef(pictures);
  latest.current = pictures;
  const changed = useRef(onChange);
  changed.current = onChange;

  const update = useCallback((next: DraftPicture[]) => {
    latest.current = next;
    setPictures(next);
    changed.current?.(next);
  }, []);

  const add = useCallback(
    async (files: File[]) => {
      if (!files.length) return;
      setLoading(true);
      try {
        const { pictures: added, errors } = await loadPictures(files, latest.current.length);
        const room = Math.max(0, MAX_PICTURES - latest.current.length);
        const kept = added.slice(0, room);
        revokePictures(added.slice(room));
        if (kept.length) update([...latest.current, ...kept]);
        setError(errors.length ? errors.join(" ") : null);
      } finally {
        setLoading(false);
      }
    },
    [update],
  );

  const remove = useCallback(
    (id: string) => {
      const gone = latest.current.filter((p) => p.id === id);
      revokePictures(gone);
      update(latest.current.filter((p) => p.id !== id));
      setError(null);
    },
    [update],
  );

  /** Forget the draft without revoking previews (they moved elsewhere). */
  const release = useCallback(() => {
    update([]);
    setError(null);
  }, [update]);

  const replace = useCallback(
    (next: DraftPicture[]) => {
      update(next);
      setError(null);
    },
    [update],
  );

  return { pictures, error, setError, loading, add, remove, release, replace, full: pictures.length >= MAX_PICTURES };
}

/** Drag-and-drop of image files onto a region. */
export function useDropZone(onFiles: (files: File[]) => void, enabled = true) {
  const [dragging, setDragging] = useState(false);
  const depth = useRef(0);
  useEffect(() => {
    if (!enabled) {
      depth.current = 0;
      setDragging(false);
    }
  }, [enabled]);
  const hasFiles = (e: DragEvent) => Array.from(e.dataTransfer?.types ?? []).includes("Files");
  return {
    dragging,
    handlers: {
      onDragEnter: (e: DragEvent) => {
        if (!enabled || !hasFiles(e)) return;
        e.preventDefault();
        depth.current++;
        setDragging(true);
      },
      onDragOver: (e: DragEvent) => {
        if (!enabled || !hasFiles(e)) return;
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
      },
      onDragLeave: (e: DragEvent) => {
        if (!enabled || !hasFiles(e)) return;
        depth.current = Math.max(0, depth.current - 1);
        if (depth.current === 0) setDragging(false);
      },
      onDrop: (e: DragEvent) => {
        if (!enabled || !hasFiles(e)) return;
        e.preventDefault();
        depth.current = 0;
        setDragging(false);
        onFiles(filesFrom(e.dataTransfer));
      },
    },
  };
}

/** Removable thumbnails of the staged pictures. */
export function PictureStrip({
  pictures,
  onRemove,
  disabled,
}: {
  pictures: readonly DraftPicture[];
  onRemove: (id: string) => void;
  disabled?: boolean;
}) {
  if (!pictures.length) return null;
  return (
    <ul className="picture-strip" aria-label="Attached pictures">
      {pictures.map((p) => (
        <li key={p.id} className="picture-draft" title={`${p.filename} · ${formatBytes(p.data.length)}${p.note ? ` · ${p.note}` : ""}`}>
          <Thumb picture={p} />
          <span className="picture-size">{formatBytes(p.data.length)}</span>
          <button
            type="button"
            className="picture-remove"
            aria-label={`Remove ${p.filename}`}
            disabled={disabled}
            onClick={() => onRemove(p.id)}
          >
            ×
          </button>
        </li>
      ))}
    </ul>
  );
}

function Thumb({ picture }: { picture: DraftPicture }) {
  const [broken, setBroken] = useState(false);
  if (!picture.previewUrl || broken) {
    return <span className="picture-fallback">{picture.filename}</span>;
  }
  return <img src={picture.previewUrl} alt={picture.filename} onError={() => setBroken(true)} />;
}

/** Paperclip button with a hidden file input. */
export function AttachButton({
  onFiles,
  disabled,
  full,
}: {
  onFiles: (files: File[]) => void;
  disabled?: boolean;
  full?: boolean;
}) {
  const input = useRef<HTMLInputElement>(null);
  return (
    <>
      <button
        type="button"
        className="btn ghost attach"
        disabled={disabled || full}
        title={full ? `Up to ${MAX_PICTURES} pictures` : "Attach pictures (or paste / drop them)"}
        aria-label="Attach pictures"
        onClick={() => input.current?.click()}
      >
        <svg width="16" height="16" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <path
            d="M21.4 11.1l-8.5 8.5a5.5 5.5 0 0 1-7.8-7.8l8.5-8.5a3.7 3.7 0 0 1 5.2 5.2l-8.5 8.5a1.8 1.8 0 0 1-2.6-2.6l7.8-7.8"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
      <input
        ref={input}
        type="file"
        accept={PICTURE_ACCEPT}
        multiple
        hidden
        onChange={(e) => {
          const files = Array.from(e.target.files ?? []);
          e.target.value = "";
          onFiles(files);
        }}
      />
    </>
  );
}
