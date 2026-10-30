// An image that opens full-window on click. Viewer panes (inspector, Files)
// fit pictures to their width, which shrinks screenshots; the lightbox fits
// the picture to the window and zooms like an image viewer: wheel or pinch
// around the pointer, drag to pan, click to toggle zoom, +/−/0 keys, and a
// click on the backdrop closes it.
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type CSSProperties,
  type SyntheticEvent,
} from "react";
import { createPortal } from "react-dom";
import { Modal } from "./Modal";
import {
  clampView,
  clickZoomScale,
  fitScale,
  fitView,
  isZoomed,
  MAX_ZOOM,
  panBy,
  wheelFactor,
  zoomTo,
  type Box,
  type View,
} from "./zoom";

export function ExpandableImage({
  src,
  alt,
  onError,
  imgStyle,
}: {
  src: string;
  alt: string;
  onError?: () => void;
  /** Sizing for the inline picture (not the lightbox). */
  imgStyle?: CSSProperties;
}) {
  const [open, setOpen] = useState(false);
  const close = useCallback(() => setOpen(false), []);
  return (
    <>
      <button type="button" className="expandable-image" title="Expand" data-track="image.expand" onClick={() => setOpen(true)}>
        <img src={src} alt={alt} onError={onError} style={imgStyle} />
      </button>
      {/* Portalled so the dialog never nests in phrasing content (markdown
          paragraphs); React events still bubble to the opener's ancestors
          (a transcript row's summary), so clicks stop at the portal root. */}
      {open &&
        createPortal(
          <div className="lightbox-portal" onClick={stop} onPointerDown={stop}>
            <Modal open onClose={close} title={alt} className="image-lightbox">
              <Lightbox src={src} alt={alt} onClose={close} />
            </Modal>
          </div>,
          document.body,
        )}
    </>
  );
}

const stop = (e: SyntheticEvent) => e.stopPropagation();

const STEP = 1.5;
const TAP_SLOP = 4;

interface Gesture {
  moved: number;
  onImage: boolean;
  multi: boolean;
}

function Lightbox({ src, alt, onClose }: { src: string; alt: string; onClose: () => void }) {
  const stageRef = useRef<HTMLDivElement>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  const [stage, setStage] = useState<Box>({ w: 0, h: 0 });
  const [img, setImg] = useState<Box>({ w: 0, h: 0 });
  // null follows the fitted view (including across window resizes).
  const [view, setView] = useState<View | null>(null);
  const [animate, setAnimate] = useState(false);
  const v = view ? clampView(view, img, stage) : fitView(img, stage);

  // Handlers read the latest geometry here; `apply` updates it immediately so
  // several wheel events between renders compound.
  const latest = useRef({ v, img, stage });
  useLayoutEffect(() => {
    latest.current = { v, img, stage };
  });
  const apply = useCallback((f: (v: View, img: Box, stage: Box) => View, smooth = false) => {
    const g = latest.current;
    const next = f(g.v, g.img, g.stage);
    g.v = next;
    setAnimate(smooth);
    setView(next);
  }, []);

  useLayoutEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const measure = () => setStage({ w: el.clientWidth, h: el.clientHeight });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const loaded = useCallback(() => {
    const el = imgRef.current;
    if (!el) return;
    // SVG without intrinsic dimensions reports 0: treat it as stage-sized.
    const s = stageRef.current;
    setImg({ w: el.naturalWidth || s?.clientWidth || 300, h: el.naturalHeight || s?.clientHeight || 150 });
  }, []);
  useLayoutEffect(() => {
    if (imgRef.current?.complete) loaded();
  }, [loaded]);

  const point = (clientX: number, clientY: number) => {
    const r = stageRef.current!.getBoundingClientRect();
    return { x: clientX - r.left, y: clientY - r.top };
  };
  const center = () => ({ x: latest.current.stage.w / 2, y: latest.current.stage.h / 2 });

  const zoomBy = useCallback(
    (factor: number, px: number, py: number, smooth: boolean) => apply((v, i, s) => zoomTo(v, v.s * factor, px, py, i, s), smooth),
    [apply],
  );
  const fit = useCallback(() => {
    setAnimate(true);
    setView(null);
  }, []);

  // Wheel and Safari's gesture events must be non-passive to stop page zoom.
  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const p = point(e.clientX, e.clientY);
      zoomBy(wheelFactor(e.deltaY, e.deltaMode, e.ctrlKey), p.x, p.y, false);
    };
    let last = 1;
    const onGestureStart = (e: Event) => {
      e.preventDefault();
      last = 1;
    };
    const onGestureChange = (e: Event) => {
      e.preventDefault();
      const g = e as Event & { scale: number; clientX: number; clientY: number };
      const p = point(g.clientX, g.clientY);
      zoomBy(g.scale / last, p.x, p.y, false);
      last = g.scale;
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    el.addEventListener("gesturestart", onGestureStart);
    el.addEventListener("gesturechange", onGestureChange);
    return () => {
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("gesturestart", onGestureStart);
      el.removeEventListener("gesturechange", onGestureChange);
    };
  }, [zoomBy]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.ctrlKey || e.metaKey || e.altKey) return;
      const c = center();
      if (e.key === "+" || e.key === "=") zoomBy(STEP, c.x, c.y, true);
      else if (e.key === "-") zoomBy(1 / STEP, c.x, c.y, true);
      else if (e.key === "0") fit();
      else return;
      e.preventDefault();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [zoomBy, fit]);

  // Pointers: one drags (pans), two pinch; a press that barely moves is a
  // click — on the image it toggles zoom, on the backdrop it closes.
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const gesture = useRef<Gesture>({ moved: 0, onImage: false, multi: false });
  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "mouse" && e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    const p = pointers.current;
    p.set(e.pointerId, point(e.clientX, e.clientY));
    if (p.size === 1) gesture.current = { moved: 0, onImage: e.target === imgRef.current, multi: false };
    else gesture.current.multi = true;
  };
  const onPointerMove = (e: ReactPointerEvent<HTMLDivElement>) => {
    const p = pointers.current;
    const prev = p.get(e.pointerId);
    if (!prev) return;
    const q = point(e.clientX, e.clientY);
    if (p.size === 1) {
      gesture.current.moved += Math.hypot(q.x - prev.x, q.y - prev.y);
      apply((v, i, s) => panBy(v, q.x - prev.x, q.y - prev.y, i, s));
    } else if (p.size === 2) {
      const other = [...p.entries()].find(([id]) => id !== e.pointerId)![1];
      const d0 = Math.hypot(prev.x - other.x, prev.y - other.y);
      const d1 = Math.hypot(q.x - other.x, q.y - other.y);
      const m0 = { x: (prev.x + other.x) / 2, y: (prev.y + other.y) / 2 };
      const m1 = { x: (q.x + other.x) / 2, y: (q.y + other.y) / 2 };
      apply((v, i, s) => {
        const zoomed = d0 > 0 ? zoomTo(v, (v.s * d1) / d0, m0.x, m0.y, i, s) : v;
        return panBy(zoomed, m1.x - m0.x, m1.y - m0.y, i, s);
      });
    }
    p.set(e.pointerId, q);
  };
  const onPointerEnd = (e: ReactPointerEvent<HTMLDivElement>) => {
    const p = pointers.current;
    if (!p.delete(e.pointerId) || p.size > 0) return;
    const g = gesture.current;
    if (e.type !== "pointerup" || g.multi || g.moved > TAP_SLOP) return;
    if (!g.onImage) {
      onClose();
      return;
    }
    const q = point(e.clientX, e.clientY);
    apply((v, i, s) => (isZoomed(v, i, s) ? fitView(i, s) : zoomTo(v, clickZoomScale(i, s), q.x, q.y, i, s)), true);
  };

  const ready = img.w > 0 && stage.w > 0;
  const zoomed = isZoomed(v, img, stage);
  const atFit = !zoomed;
  const atMax = v.s >= Math.max(MAX_ZOOM, fitScale(img, stage)) * 0.999;
  const c = center;
  return (
    <>
      <div
        ref={stageRef}
        className="lightbox-stage"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerEnd}
        onPointerCancel={onPointerEnd}
      >
        <img
          ref={imgRef}
          src={src}
          alt={alt}
          draggable={false}
          onLoad={loaded}
          className={`lightbox-img${animate ? " animate" : ""}${zoomed ? " zoomed" : ""}`}
          style={{
            width: img.w || undefined,
            height: img.h || undefined,
            visibility: ready ? "visible" : "hidden",
            transform: `translate(${v.x}px, ${v.y}px) scale(${v.s})`,
          }}
        />
      </div>
      <div className="lightbox-actions">
        <span className="lightbox-zoom" role="group" aria-label="Zoom">
          <button type="button" className="btn ghost small" aria-label="Zoom out" title="Zoom out (−)" disabled={atFit} onClick={() => zoomBy(1 / STEP, c().x, c().y, true)}>
            −
          </button>
          <button type="button" className="btn ghost small lightbox-level" title="Fit to window (0)" disabled={atFit} onClick={fit}>
            {Math.round(v.s * 100)}%
          </button>
          <button type="button" className="btn ghost small" aria-label="Zoom in" title="Zoom in (+)" disabled={atMax} onClick={() => zoomBy(STEP, c().x, c().y, true)}>
            +
          </button>
        </span>
        <button
          type="button"
          className="btn ghost small"
          disabled={Math.abs(v.s - 1) < 0.001}
          onClick={() => apply((v, i, s) => zoomTo(v, 1, s.w / 2, s.h / 2, i, s), true)}
        >
          Actual size
        </button>
        {/* Browsers block top-level navigation to data: URLs (SVG previews). */}
        {!src.startsWith("data:") && (
          <a className="btn ghost small" href={src} target="_blank" rel="noreferrer">
            Open in new tab
          </a>
        )}
      </div>
    </>
  );
}
