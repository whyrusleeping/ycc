// Composer picture rules: type sniffing, add planning (type + count caps),
// downscale decisions, and the wire shape.
import { describe, expect, it } from "vitest";
import {
  MAX_PICTURES,
  MAX_PICTURE_BYTES,
  declaredType,
  fitWithin,
  formatBytes,
  isPictureType,
  needsDownscale,
  pictureFilename,
  planAdd,
  sniffPictureType,
  toImageAttachments,
} from "../src/features/attachments/attachments";

const bytes = (...b: number[]) => new Uint8Array([...b, ...new Array(16).fill(0)]);
const PNG = bytes(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a);
const JPEG = bytes(0xff, 0xd8, 0xff, 0xe0);
const GIF = bytes(0x47, 0x49, 0x46, 0x38, 0x39, 0x61);
const WEBP = bytes(0x52, 0x49, 0x46, 0x46, 1, 2, 3, 4, 0x57, 0x45, 0x42, 0x50, 0x56, 0x50);

describe("attachments", () => {
  it("sniffs the four daemon-accepted picture types", () => {
    expect(sniffPictureType(PNG)).toBe("image/png");
    expect(sniffPictureType(JPEG)).toBe("image/jpeg");
    expect(sniffPictureType(GIF)).toBe("image/gif");
    expect(sniffPictureType(WEBP)).toBe("image/webp");
    expect(sniffPictureType(new TextEncoder().encode("hello world, not a picture"))).toBeNull();
    expect(sniffPictureType(new Uint8Array([0x89, 0x50]))).toBeNull();
  });

  it("declares the sniffed type over a mislabeled one, else trusts the browser", () => {
    expect(declaredType(JPEG, "image/png")).toBe("image/jpeg");
    expect(declaredType(new Uint8Array([1, 2, 3]), "IMAGE/PNG")).toBe("image/png");
  });

  it("refuses non-pictures and caps the draft at four", () => {
    const pic = (name: string) => ({ name, type: "image/png", size: 10 });
    expect(planAdd(0, [pic("a"), { name: "notes.pdf", type: "application/pdf", size: 1 }, pic("b")])).toEqual({
      accept: [0, 2],
      errors: ["Only JPEG, PNG, GIF, and WebP pictures can be attached (skipped notes.pdf)."],
    });
    const plan = planAdd(3, [pic("a"), pic("b")]);
    expect(plan.accept).toEqual([0]);
    expect(plan.errors).toEqual([`You can attach up to ${MAX_PICTURES} pictures.`]);
    expect(planAdd(MAX_PICTURES, [pic("a")]).accept).toEqual([]);
    expect(planAdd(1, [])).toEqual({ accept: [], errors: [] });
    expect(isPictureType("image/svg+xml")).toBe(false);
  });

  it("downscales only oversized still pictures, never upscaling", () => {
    expect(needsDownscale({ type: "image/png", size: MAX_PICTURE_BYTES + 1 })).toBe(true);
    expect(needsDownscale({ type: "image/png", size: MAX_PICTURE_BYTES })).toBe(false);
    expect(needsDownscale({ type: "image/gif", size: MAX_PICTURE_BYTES * 2 })).toBe(false);
    expect(fitWithin(4000, 3000, 2048)).toEqual({ width: 2048, height: 1536 });
    expect(fitWithin(1000, 3000, 2048)).toEqual({ width: 683, height: 2048 });
    expect(fitWithin(800, 600, 2048)).toEqual({ width: 800, height: 600 });
  });

  it("names pasted and re-encoded pictures", () => {
    expect(pictureFilename("image.png", 0, "image/png")).toBe("pasted-1.png");
    expect(pictureFilename("", 2, "image/jpeg")).toBe("pasted-3.jpg");
    expect(pictureFilename("shot.png", 0, "image/jpeg")).toBe("shot.jpg");
    expect(pictureFilename("C:\\tmp\\photo.webp", 0, "image/webp")).toBe("photo.webp");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2 KB");
    expect(formatBytes(6 * 1024 * 1024)).toBe("6.0 MB");
  });

  it("maps drafts to ImageAttachment fields", () => {
    const data = new Uint8Array([1]);
    expect(
      toImageAttachments([{ id: "x", data, mediaType: "image/png", filename: "a.png", previewUrl: "blob:x", note: "n" }]),
    ).toEqual([{ data, mediaType: "image/png", filename: "a.png" }]);
  });
});
