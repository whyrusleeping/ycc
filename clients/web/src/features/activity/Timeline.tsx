// The swimlane timeline: one lane per node (coordinator, agents, jobs), bars
// for agent rounds and job lifetimes coloured by status. Running bars end at
// "now" and grow as the panel re-renders. The track can zoom (scrolling
// horizontally, following the live edge) so short rounds in a long session
// stay legible; bars never shrink below MIN_BAR_PX.
import { memo, useCallback, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type FocusEvent, type MouseEvent } from "react";
import type { ActivityNode } from "../../gen/ycc/v1/ycc_pb";
import {
  barGeometry,
  formatDuration,
  isJob,
  laneIntervals,
  laneOrder,
  statusOf,
  ticks,
  ROOT_ID,
  type Bar,
  type TimeRange,
} from "./model";
import { StatusDot } from "./StatusDot";

const LABEL_W = 112;
const ZOOMS = [1, 2, 4, 8, 16, 32, 64];

interface Tip {
  node: ActivityNode;
  bar: Bar;
  x: number;
  y: number;
}

type ShowTip = (e: MouseEvent | FocusEvent, node: ActivityNode, bar: Bar) => void;

/** Memoized: it re-renders on new activity data, the clock tick, or selection only. */
export const Timeline = memo(function Timeline({
  nodes,
  range,
  selected,
  onSelect,
}: {
  nodes: readonly ActivityNode[];
  range: TimeRange;
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [zoom, setZoom] = useState(1);
  const [tip, setTip] = useState<Tip | null>(null);
  // Follow the live edge while the reader is at the right end.
  const atEnd = useRef(true);
  const lastTrack = useRef(0);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const measure = () => setWidth(Math.max(0, el.clientWidth - LABEL_W));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const track = width * zoom;
  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    if (atEnd.current) el.scrollLeft = el.scrollWidth;
    else if (lastTrack.current > 0 && lastTrack.current !== track) {
      // Zooming keeps the time under the view's centre in place.
      const centre = (el.scrollLeft + el.clientWidth / 2 - LABEL_W) / lastTrack.current;
      el.scrollLeft = centre * track + LABEL_W - el.clientWidth / 2;
    }
    lastTrack.current = track;
  }, [track, range]);

  const lanes = useMemo(() => laneOrder(nodes), [nodes]);
  const axis = useMemo(() => ticks(range, track), [range, track]);
  // Lane grid lines repeat at the tick step (a CSS background, not elements).
  const step = axis.length > 1 ? axis[1].x - axis[0].x : 0;
  const zi = ZOOMS.indexOf(zoom);

  // The tip is fixed-position (viewport coordinates) so the scrolling
  // overview cannot clip it. Hovering re-renders only this component; the
  // memoized lanes are skipped.
  const showTip = useCallback<ShowTip>((e, node, bar) => {
    const target = (e.currentTarget as HTMLElement).getBoundingClientRect();
    const x = "clientX" in e ? e.clientX : target.left + target.width / 2;
    setTip({ node, bar, x, y: target.bottom + 6 });
  }, []);
  const hideTip = useCallback(() => setTip(null), []);

  return (
    <section className="activity-timeline" aria-label="Timeline">
      <div className="tl-toolbar">
        <span className="label">Timeline</span>
        <span className="tl-span muted">
          {formatDuration(range.end - range.start)}
          {range.live ? " · live" : ""}
        </span>
        <span className="tl-zoom" role="group" aria-label="Zoom">
          <button type="button" className="btn ghost small" disabled={zi <= 0} aria-label="Zoom out" title="Zoom out" data-track="activity.zoom_out" onClick={() => setZoom(ZOOMS[zi - 1])}>
            −
          </button>
          <button type="button" className="btn ghost small tl-zoom-level" disabled={zoom === 1} title="Fit the whole session" data-track="activity.zoom_fit" onClick={() => setZoom(1)}>
            {zoom === 1 ? "Fit" : `${zoom}×`}
          </button>
          <button type="button" className="btn ghost small" disabled={zi >= ZOOMS.length - 1} aria-label="Zoom in" title="Zoom in" data-track="activity.zoom_in" onClick={() => setZoom(ZOOMS[zi + 1])}>
            +
          </button>
        </span>
      </div>
      <div
        className="tl-scroll"
        ref={scroller}
        onScroll={(e) => {
          const el = e.currentTarget;
          atEnd.current = el.scrollWidth - el.scrollLeft - el.clientWidth < 4;
          setTip(null);
        }}
      >
        <div
          className={`tl-grid${step > 0 ? " gridded" : ""}`}
          style={{ width: LABEL_W + track, ...(step > 0 ? ({ "--tl-step": `${step}px` } as CSSProperties) : {}) }}
        >
          <div className="tl-row tl-axis" aria-hidden="true">
            <div className="tl-label" />
            <div className="tl-track">
              {axis.map((t) => (
                <span key={t.x} className="tl-tick" style={{ left: t.x }}>
                  {/* A label that would overflow the track would also widen the scroller. */}
                  {t.x < track - 30 && <span className="tl-tick-label">{t.label}</span>}
                </span>
              ))}
            </div>
          </div>
          {lanes.map((node) => (
            <Lane
              key={node.id}
              node={node}
              range={range}
              track={width > 0 ? track : 0}
              selected={node.id === selected}
              onSelect={onSelect}
              showTip={showTip}
              hideTip={hideTip}
            />
          ))}
          {range.live && <span className="tl-now" style={{ left: LABEL_W + track - 2 }} aria-hidden="true" />}
        </div>
      </div>
      {tip && <BarTip tip={tip} range={range} />}
    </section>
  );
});

const Lane = memo(function Lane({
  node,
  range,
  track,
  selected,
  onSelect,
  showTip,
  hideTip,
}: {
  node: ActivityNode;
  range: TimeRange;
  /** Track width in pixels; 0 before the first measurement. */
  track: number;
  selected: boolean;
  onSelect: (id: string) => void;
  showTip: ShowTip;
  hideTip: () => void;
}) {
  const bars = track > 0 ? barGeometry(laneIntervals(node, range), range, track) : [];
  const job = isJob(node);
  return (
    <div className={`tl-row tl-lane${selected ? " selected" : ""}${node.id === ROOT_ID ? " root" : ""}`}>
      <button
        type="button"
        className={`tl-label${job ? " mono" : ""}`}
        title={node.label}
        aria-pressed={selected}
        data-track="activity.timeline_lane"
        onClick={() => onSelect(node.id)}
      >
        <StatusDot status={statusOf(node.status)} />
        <span className="tl-label-text">{node.label || node.id}</span>
      </button>
      <div className="tl-track">
        {bars.map((b, i) => (
          <button
            key={i}
            type="button"
            className={`tl-bar st-${b.status}${b.open ? " open" : ""}${b.unknownEnd ? " unknown-end" : ""}${node.id === ROOT_ID ? " root" : ""}`}
            style={{ left: b.x, width: b.w }}
            aria-label={barText(node, b)}
            data-track="activity.timeline_bar"
            onClick={() => onSelect(node.id)}
            onMouseEnter={(e) => showTip(e, node, b)}
            onMouseMove={(e) => showTip(e, node, b)}
            onMouseLeave={hideTip}
            onFocus={(e) => showTip(e, node, b)}
            onBlur={hideTip}
          />
        ))}
      </div>
    </div>
  );
});

function barText(node: ActivityNode, b: Bar): string {
  const what = b.round > 0 ? `${node.label} round ${b.round}` : node.label;
  return `${what}: ${b.status}, ${formatDuration(b.end - b.start)}`;
}

function BarTip({ tip, range }: { tip: Tip; range: TimeRange }) {
  const { node, bar } = tip;
  const W = 260;
  const left = Math.max(4, Math.min(tip.x - W / 2, window.innerWidth - W - 4));
  // Near the bottom of the window, show it above the bar instead.
  const below = tip.y + 110 < window.innerHeight;
  const job = isJob(node);
  return (
    <div
      className="tl-tip"
      role="tooltip"
      style={below ? { left, top: tip.y, width: W } : { left, bottom: window.innerHeight - tip.y + 30, width: W }}
    >
      <div className={`tl-tip-title${job ? " mono" : ""}`}>{node.label || node.id}</div>
      <div className="tl-tip-meta">
        <span className={`act-status st-${bar.status}`}>{bar.status}</span>
        {bar.round > 0 && <span>round {bar.round}</span>}
        <span>{bar.open ? `${formatDuration(bar.end - bar.start)} so far` : bar.unknownEnd ? "end unknown" : formatDuration(bar.end - bar.start)}</span>
      </div>
      <div className="tl-tip-meta muted">
        started {new Date(bar.start).toLocaleTimeString()} (+{formatDuration(bar.start - range.start)})
      </div>
      {node.model && !job && <div className="tl-tip-meta muted">{node.model}</div>}
    </div>
  );
}
