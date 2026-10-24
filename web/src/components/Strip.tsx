import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { Metrics, SearchResult, SegmentKind, SegmentOutcome } from '../lib/api';
import { formatBytes, n, shortId } from '../lib/format';
import { plan } from '../lib/stages';
import s from './Strip.module.css';

const KIND_TEXT: Record<SegmentKind, string> = {
  time: 'Skipped by time range',
  bloom: 'Skipped by bloom filter',
  scanned: 'Read',
};
const WHY: Record<SegmentKind, string> = {
  time: 'Outside the time range, so ruled out from the manifest. Not opened.',
  bloom: 'Its bloom filter says a searched word is not in it. Not opened.',
  scanned: '',
};

interface Props {
  result: SearchResult;
  metrics: Metrics;
  hot?: string | null; // a segment pointed at elsewhere (the read list)
  picked?: string | null; // the read segment whose lines are shown
  onHot?: (id: string | null) => void;
  onPick?: (id: string | null) => void; // without it, bars only explain themselves
}

let plays = 0; // numbers each result, so a new one remounts the bars and plays again

// Every segment as one bar, oldest on the left: a segment that was read stands
// tall, a skipped one stays low, so a good search is a flat line with a few
// spikes. Each result plays once in the order the search worked: the time
// range rules its segments out, then the bloom filters, then the reads stand
// up. Pointing at a bar (or arrowing to it) says what happened to it; clicking
// a read one shows only its lines. If the server judged there were too many
// to list, the three totals are drawn as proportional blocks instead.
export function Strip({ result, metrics, hot = null, picked = null, onHot, onPick }: Props) {
  const segs = result.segments ?? [];
  const total = metrics.segments_considered;
  const ref = useRef<HTMLDivElement>(null);
  const tip = useRef<HTMLSpanElement>(null);
  const [dense, setDense] = useState(false);
  const [active, setActive] = useState<number | null>(null);
  const [tipX, setTipX] = useState(0);
  const play = useMemo(() => ++plays, [result]);
  const { delays } = useMemo(() => plan(segs.map((g) => g.kind)), [segs]);

  // Bars are at least 1px wide with a 2px gap; when that doesn't fit (hundreds
  // of segments, or a phone), drop the gaps so nothing overflows.
  useLayoutEffect(() => {
    const el = ref.current;
    if (el) setDense(segs.length * 3 > el.clientWidth);
    setActive(null);
  }, [result, segs.length]);

  // Keep the tooltip over its bar but inside the strip's panel: measured,
  // since its width depends on the text.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || active === null) return;
    const panel = el.closest('section') ?? el;
    const box = el.getBoundingClientRect();
    const room = panel.getBoundingClientRect();
    const half = (tip.current?.offsetWidth ?? 240) / 2 + 8;
    const x = ((active + 0.5) / segs.length) * box.width;
    setTipX(Math.min(Math.max(x, room.left - box.left + half), room.right - box.left - half));
  }, [active, segs.length]);

  // Pointing at a bar also lights its row in the read list.
  function point(i: number | null) {
    setActive(i);
    onHot?.(i === null ? null : segs[i].id);
  }
  function pick(g: SegmentOutcome) {
    if (onPick && g.kind === 'scanned') onPick(picked === g.id ? null : g.id);
  }

  // A tapped bar keeps its tooltip until something else is tapped.
  useEffect(() => {
    if (active === null) return;
    const away = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) point(null);
    };
    document.addEventListener('pointerdown', away);
    return () => document.removeEventListener('pointerdown', away);
  });

  const label =
    `${n(total)} segments: ${n(metrics.skipped_by_time)} skipped by time range, ` +
    `${n(metrics.skipped_by_bloom)} skipped by bloom filter, ${n(metrics.segments_scanned)} read.`;

  if (segs.length === 0) {
    const blocks: [string, number][] = [
      [s.time, metrics.skipped_by_time],
      [s.bloom, metrics.skipped_by_bloom],
      [s.scanned, metrics.segments_scanned],
    ];
    return (
      <div className={s.box} data-blocks>
        <div ref={ref} className={`${s.strip} ${s.blocks}`} role="img" aria-label={label}>
          {blocks.map(([cls, v]) =>
            v ? <i key={cls} className={cls} style={{ flexBasis: (v / total) * 100 + '%' }} /> : null,
          )}
        </div>
        {result.segments_truncated && <p className={s.note}>Too many segments to draw one by one, so they are shown as proportions.</p>}
      </div>
    );
  }

  const anyRead = segs.some((g) => g.kind === 'scanned');
  const g = active === null ? null : segs[active];
  return (
    <div className={s.box} style={{ '--n': segs.length } as React.CSSProperties}>
      <div
        ref={ref}
        key={play}
        className={[s.strip, dense && s.dense, !anyRead && s.flat, onPick && s.pickable].filter(Boolean).join(' ')}
        role="img"
        tabIndex={0}
        aria-label={label + ' Use the arrow keys for each segment' + (onPick && anyRead ? ", and Enter to show a read segment's lines." : '.')}
        onPointerLeave={(e) => e.pointerType !== 'touch' && point(null)}
        onBlur={() => point(null)}
        onKeyDown={(e) => {
          const last = segs.length - 1;
          const move: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1, Home: -Infinity, End: Infinity };
          if (e.key in move) {
            e.preventDefault();
            const from = active ?? (move[e.key] > 0 ? -1 : last + 1);
            point(Math.min(Math.max(from + move[e.key], 0), last));
          } else if ((e.key === 'Enter' || e.key === ' ') && g) {
            e.preventDefault();
            pick(g);
          } else if (e.key === 'Escape') point(null);
        }}
      >
        {segs.map((seg, i) => (
          <span
            key={seg.id}
            className={s.slot}
            data-active={active === i || hot === seg.id || undefined}
            data-picked={picked === seg.id || undefined}
            onPointerEnter={() => point(i)}
            onClick={() => {
              point(i);
              pick(seg);
            }}
          >
            <i className={s[seg.kind]} style={{ '--d': delays[i] + 'ms' } as React.CSSProperties} />
          </span>
        ))}
        {g && (
          <span ref={tip} className={s.tip} style={{ left: tipX }} aria-live="polite">
            <span className={s.tipHead}>
              <i className={s[g.kind]} aria-hidden="true" />
              <b>{KIND_TEXT[g.kind]}</b>
              <span className="mono">{g.kind === 'scanned' ? formatBytes(g.bytes_read) : `${formatBytes(g.size)} not downloaded`}</span>
            </span>
            <span className="mono">
              {g.first} – {g.first.slice(0, 10) === g.last.slice(0, 10) ? g.last.slice(11) : g.last} UTC
            </span>
            <span>
              {g.kind === 'scanned'
                ? g.hits
                  ? `Supplied ${n(g.hits)} of the lines shown.`
                  : 'Supplied none of the lines shown.'
                : WHY[g.kind]}
              {g.kind === 'scanned' && onPick && (
                <span className={s.tipHint}>
                  {' '}
                  <span className={s.fine}>{picked === g.id ? 'Click again to show all lines.' : 'Click to show only its lines.'}</span>
                  <span className={s.coarse}>{picked === g.id ? 'Tap again to show all lines.' : 'Tap to show only its lines.'}</span>
                </span>
              )}
            </span>
            <span className={`${s.tipId} mono`}>segment {shortId(g.id)}</span>
          </span>
        )}
      </div>
      <p className={`${s.axis} mono`}>
        <span>{segs[0].first.slice(0, 10)}</span>
        <span>{segs[segs.length - 1].last.slice(0, 10)}</span>
      </p>
    </div>
  );
}
