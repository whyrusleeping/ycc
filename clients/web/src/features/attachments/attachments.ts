// Picture-attachment rules shared by the live composer and the new-session
// prompt. They mirror the daemon's policy (internal/server checkInputImages:
// at most four JPEG/PNG/GIF/WebP pictures, each 1 byte–5 MiB, content sniffed
// against the declared type) so the client can refuse obvious misses early.
// The daemon stays the authority: anything that passes here is still
// validated server-side and its error is shown verbatim.

export const MAX_PICTURES = 4;
export const MAX_PICTURE_BYTES = 5 * 1024 * 1024;
/** Long edge an oversized picture is downscaled to before re-encoding. */
export const DOWNSCALE_MAX_EDGE = 2048;

export const PICTURE_TYPES = ["image/jpeg", "image/png", "image/gif", "image/webp"] as const;
export type PictureType = (typeof PICTURE_TYPES)[number];
export const PICTURE_ACCEPT = PICTURE_TYPES.join(",");

/** A picture staged in a composer, ready to travel as an ImageAttachment. */
export interface DraftPicture {
  id: string;
  data: Uint8Array;
  mediaType: string;
  filename: string;
  /** Object URL for the thumbnail; null when the browser can't render it. */
  previewUrl: string | null;
  /** Set when the client re-encoded an oversized original. */
  note?: string;
}

/** What a file looks like before its bytes are read. */
export interface Candidate {
  name: string;
  type: string;
  size: number;
}

export function isPictureType(type: string): type is PictureType {
  return (PICTURE_TYPES as readonly string[]).includes(type.toLowerCase());
}

/**
 * Sniff a picture type from its magic bytes (the same signatures Go's
 * http.DetectContentType uses for these four types). Null when unknown.
 */
export function sniffPictureType(bytes: Uint8Array): PictureType | null {
  const b = bytes;
  const at = (i: number, ...sig: number[]) => sig.every((v, k) => b[i + k] === v);
  if (b.length >= 8 && at(0, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)) return "image/png";
  if (b.length >= 3 && at(0, 0xff, 0xd8, 0xff)) return "image/jpeg";
  if (b.length >= 6 && (at(0, 0x47, 0x49, 0x46, 0x38, 0x37, 0x61) || at(0, 0x47, 0x49, 0x46, 0x38, 0x39, 0x61))) {
    return "image/gif";
  }
  if (b.length >= 14 && at(0, 0x52, 0x49, 0x46, 0x46) && at(8, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50)) return "image/webp";
  return null;
}

/**
 * The media type to declare for a picture: its sniffed type when recognisable
 * (a mislabeled extension must not fail the daemon's content check), else the
 * browser-reported type — which the daemon then judges.
 */
export function declaredType(bytes: Uint8Array, reported: string): string {
  return sniffPictureType(bytes) ?? reported.toLowerCase();
}

export interface AddPlan {
  /** Indices into the candidate list to read and stage, in order. */
  accept: number[];
  /** User-facing reasons some candidates were skipped. */
  errors: string[];
}

/**
 * Decide which dropped/pasted/picked files to stage given `current` staged
 * pictures: non-picture files are refused and the draft is capped at
 * MAX_PICTURES (an empty round never touches the draft).
 */
export function planAdd(current: number, candidates: Candidate[], limit = MAX_PICTURES): AddPlan {
  const errors: string[] = [];
  const accept: number[] = [];
  const unsupported: string[] = [];
  candidates.forEach((c, i) => {
    if (!isPictureType(c.type)) unsupported.push(c.name || c.type || "file");
    else accept.push(i);
  });
  if (unsupported.length) {
    errors.push(`Only JPEG, PNG, GIF, and WebP pictures can be attached (skipped ${unsupported.join(", ")}).`);
  }
  const room = Math.max(0, limit - current);
  if (accept.length > room) {
    errors.push(`You can attach up to ${limit} pictures.`);
    accept.splice(room);
  }
  return { accept, errors };
}

/**
 * Whether a picture should be re-encoded smaller before sending: it is over
 * the daemon's size cap and is a still format the browser can redraw (an
 * animated GIF would lose its animation, so it is left for the daemon to judge).
 */
export function needsDownscale(c: { type: string; size: number }): boolean {
  return c.size > MAX_PICTURE_BYTES && c.type !== "image/gif";
}

/** Target dimensions that fit `maxEdge` on the long side (never upscales). */
export function fitWithin(width: number, height: number, maxEdge = DOWNSCALE_MAX_EDGE): { width: number; height: number } {
  const long = Math.max(width, height);
  if (!(long > maxEdge)) return { width, height };
  const scale = maxEdge / long;
  return { width: Math.max(1, Math.round(width * scale)), height: Math.max(1, Math.round(height * scale)) };
}

/** Filename for a pasted or re-encoded picture. */
export function pictureFilename(original: string, index: number, mediaType: string): string {
  const ext = mediaType === "image/jpeg" ? "jpg" : mediaType.replace("image/", "");
  const name = original.trim().replace(/^.*[\\/]/, "");
  if (!name || name === "image.png") return `pasted-${index + 1}.${ext}`;
  if (mediaType === "image/jpeg" && !/\.jpe?g$/i.test(name)) return `${name.replace(/\.[^.]*$/, "")}.jpg`;
  return name;
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** Wire shape for StartSession/SendInput `images`. */
export function toImageAttachments(pictures: readonly DraftPicture[]): { data: Uint8Array; mediaType: string; filename: string }[] {
  return pictures.map((p) => ({ data: p.data, mediaType: p.mediaType, filename: p.filename }));
}
