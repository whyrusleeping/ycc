// A markdown image that names a project file (`![chart](docs/chart.png)`, or
// an HTML `<img>` in a markdown file), read through the daemon like the file
// viewer and shown inline; it expands to the lightbox. Raster bytes become a
// blob: URL and SVG a data: URL, as in the viewer
// (docs/design/web-client.md#rendering-safety). Anything else — still
// loading, unreadable, too large, not an image — shows the alt text, linking
// to the file once it's known not to render.
import { useMemo, useState, type CSSProperties } from "react";
import { ExpandableImage } from "../../ui/ExpandableImage";
import { FileRef } from "../files/FileRef";
import type { FileReference } from "../files/fileReference";
import { isRasterPreviewType, isSvgPath, svgDataUrl } from "../files/model";
import { useBlobUrl, useFileRead } from "../files/preview";

export function MarkdownImage({
  alt,
  href,
  reference,
  project,
  sessionId,
  style,
}: {
  alt: string;
  /** The destination as written (copied/opened by the fallback link). */
  href: string;
  reference: FileReference;
  project: string;
  sessionId: string;
  /** Width/height/float from an HTML `<img>`'s attributes. */
  style?: CSSProperties;
}) {
  const read = useFileRead({ project, sessionId, path: reference.path });
  const resp = read.data;
  const raster = resp && isRasterPreviewType(resp.mediaType) && resp.data.length > 0 ? resp : undefined;
  const blobUrl = useBlobUrl(raster?.data, raster?.mediaType);
  const svgUrl = useMemo(
    () =>
      resp && !raster && !resp.isBinary && !resp.truncated && isSvgPath(resp.path || reference.path)
        ? svgDataUrl(new TextDecoder("utf-8").decode(resp.data))
        : null,
    [resp, raster, reference.path],
  );
  const [broken, setBroken] = useState(false);
  const { wrap, img } = useMemo(() => splitStyle(style), [style]);
  const src = blobUrl ?? svgUrl;
  if (src && !broken) {
    return (
      <span className="md-img" style={wrap}>
        <ExpandableImage src={src} alt={alt} onError={() => setBroken(true)} imgStyle={img} />
      </span>
    );
  }
  const label = <span className="md-image">Image: {alt}</span>;
  if (read.isPending || (raster && !blobUrl && !broken)) return label;
  return (
    <FileRef path={href} reference={reference} code={false}>
      {label}
    </FileRef>
  );
}

/**
 * An explicit width sizes the wrapper (so percentages resolve against the
 * text column) with the picture filling it; an explicit size lifts the
 * default height cap.
 */
function splitStyle(style: CSSProperties | undefined): { wrap?: CSSProperties; img?: CSSProperties } {
  if (!style) return {};
  const { width, height, ...rest } = style;
  const wrap: CSSProperties = { ...rest };
  const img: CSSProperties = {};
  if (width !== undefined) {
    wrap.width = width;
    img.width = "100%";
  }
  if (height !== undefined) img.height = height;
  if (width !== undefined || height !== undefined) img.maxHeight = "none";
  if (width !== undefined && height === undefined) img.height = "auto";
  return { wrap, img };
}
