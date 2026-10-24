import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { SystemSegment } from '../lib/api';
import { formatBytes, n, shortId } from '../lib/format';
import { blockWidths, generationText, rowsFor } from '../lib/system';
import s from './Bucket.module.css';

interface Props {
  segments: SystemSegment[]; // oldest first, including any just replaced
  fresh: Set<string>; // just sealed or just merged: they enter
  leaving: Set<string>; // just replaced by a merge: they fade out
  hot: Set<string>; // pointed at in the event log
}

// The bucket as it is: one block per segment, oldest first, each as wide as
// its file is large and toned by how many merges its lines have been through.
// Neutral on purpose: orange means "read by a search", and nothing here was.
// Pointing at a block (or arrowing along them) says what it holds.
export function Bucket({ segments, fresh, leaving, hot }: Props) {
  const ref = useRef<HTMLDivElement>(null);
  const tip = useRef<HTMLSpanElement>(null);
  const [width, setWidth] = useState(0);
  const [active, setActive] = useState<string | null>(null);
  const [tipPos, setTipPos] = useState({ x: 0, y: 0 });

  // The scale depends on the space there is.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  // The tooltip sits over its block, kept inside the panel.
  useLayoutEffect(() => {
    const el = ref.current;
    const block = active ? el?.querySelector<HTMLElement>(`[data-id="${CSS.escape(active)}"]`) : null;
    if (!el || !block) return;
    const box = el.getBoundingClientRect();
    const b = block.getBoundingClientRect();
    const half = (tip.current?.offsetWidth ?? 260) / 2;
    const x = Math.min(Math.max(b.left - box.left + b.width / 2, half), box.width - half);
    setTipPos({ x, y: b.top - box.top });
  }, [active, segments, width]);

  // A tapped block keeps its tooltip until something else is tapped.
  useEffect(() => {
    if (!active) return;
    const away = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setActive(null);
    };
    document.addEventListener('pointerdown', away);
    return () => document.removeEventListener('pointerdown', away);
  }, [active]);

  const widths = blockWidths(
    segments.map((g) => g.size),
    Math.max(width - 2, 0),
    rowsFor(segments.length),
  );
  const g = active ? segments.find((x) => x.id === active) : undefined;
  const total = segments.filter((x) => !leaving.has(x.id)).length;

  return (
    <div
      ref={ref}
      className={s.bucket}
      role="img"
      tabIndex={0}
      aria-label={`${n(total)} segments, oldest first, each as wide as its file. Use the arrow keys for each segment.`}
      onPointerLeave={(e) => e.pointerType !== 'touch' && setActive(null)}
      onBlur={() => setActive(null)}
      onKeyDown={(e) => {
        const move: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1, Home: -Infinity, End: Infinity };
        if (e.key in move) {
          e.preventDefault();
          const last = segments.length - 1;
          const at = active ? segments.findIndex((x) => x.id === active) : move[e.key] > 0 ? -1 : last + 1;
          setActive(segments[Math.min(Math.max(at + move[e.key], 0), last)]?.id ?? null);
        } else if (e.key === 'Escape') setActive(null);
      }}
    >
      {segments.map((seg, i) => (
        <span
          key={seg.id}
          data-id={seg.id}
          className={s.block}
          data-gen={Math.min(seg.generation, 2)}
          data-fresh={fresh.has(seg.id) || undefined}
          data-leaving={leaving.has(seg.id) || undefined}
          data-active={active === seg.id || hot.has(seg.id) || undefined}
          style={{ width: widths[i] }}
          onPointerEnter={() => setActive(seg.id)}
          onClick={() => setActive(seg.id)}
        />
      ))}
      {g && (
        <span ref={tip} className={s.tip} style={{ left: tipPos.x, top: tipPos.y }} aria-live="polite">
          <b>
            Segment <span className="mono">{shortId(g.id)}</span>
            {leaving.has(g.id) && ' (replaced by a merge)'}
          </b>
          <span className="mono">
            {n(g.lines)} lines · {formatBytes(g.size)}
          </span>
          <span className="mono">
            {g.first} – {g.first.slice(0, 10) === g.last.slice(0, 10) ? g.last.slice(11) : g.last} UTC
          </span>
          <span>
            {generationText(g.generation)[0].toUpperCase() + generationText(g.generation).slice(1)}; written <span className="mono">{g.created.slice(0, 19)}</span> UTC.
          </span>
        </span>
      )}
    </div>
  );
}
