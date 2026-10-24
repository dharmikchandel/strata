import { useLayoutEffect, useRef, useState } from 'react';
import type { Histogram } from '../lib/api';
import { n } from '../lib/format';
import { durationLabel } from '../lib/query';
import { ChevronIcon } from './icons';
import s from './MatchChart.module.css';

interface Props {
  histogram: Histogram;
  scanned: number; // segments read, which is where the counts come from
  // The part of the query's range that holds data, in ms; null ends fall back
  // to the buckets. The axis is labelled with these, so it agrees with the
  // stored range rather than showing the bucket edges just outside it.
  extent: [number | null, number | null];
}

const DAY = 86_400_000;
const Y_GUTTER = 44; // px: the 2.75rem column of y-axis labels left of the plot

// A bucket's start as UTC text, as precise as the bucket width needs.
function stamp(ms: number, width: number): string {
  const iso = new Date(ms).toISOString();
  if (width >= DAY) return iso.slice(0, 10);
  if (width >= 60_000) return iso.slice(0, 16).replace('T', ' ');
  return iso.slice(0, 19).replace('T', ' ');
}

function span(start: number, width: number): string {
  const a = stamp(start, width);
  const b = stamp(start + width, width);
  // Same day: "2005-06-20 13:00 – 13:01".
  return a.slice(0, 10) === b.slice(0, 10) && a.length > 10 ? `${a} – ${b.slice(11)}` : `${a} – ${b}`;
}

// The smallest round number at or above v (1, 2 or 5 times a power of ten),
// for the one gridline.
function niceCeil(v: number): number {
  if (v <= 1) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return [1, 2, 5, 10].map((m) => m * p).find((x) => x >= v)!;
}
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });

// Matches over time: one column per bucket, every match counted (not only the
// lines shown). Neutral ink, because orange means "read from storage" and these
// are matches. Hover or arrow keys give a bucket's exact count; the table under
// it has every count without either.
export function MatchChart({ histogram: h, scanned, extent }: Props) {
  const plot = useRef<HTMLDivElement>(null);
  const tip = useRef<HTMLSpanElement>(null);
  const [active, setActive] = useState<number | null>(null);
  const [tipX, setTipX] = useState(0);
  const max = Math.max(...h.counts, 0);
  const top = niceCeil(max);
  const nonZero = h.counts.filter((c) => c > 0).length;

  // Keep the tooltip over its column but inside the panel: measured, since
  // its width depends on the bucket's time text. It may reach left over the
  // y-axis labels (Y_GUTTER), not past them.
  useLayoutEffect(() => {
    const el = plot.current;
    if (!el || active === null) return;
    const w = el.clientWidth;
    const half = (tip.current?.offsetWidth ?? 180) / 2 + 4;
    setTipX(Math.min(Math.max(((active + 0.5) / h.counts.length) * w, half - Y_GUTTER), w - half));
  }, [active, h.counts.length]);

  const end = h.start_ms + h.bucket_ms * h.counts.length;
  const axisFrom = Math.max(h.start_ms, extent[0] ?? -Infinity);
  const axisTo = Math.min(end, extent[1] ?? Infinity);
  const per = durationLabel(Math.round(h.bucket_ms / 1000));

  return (
    <figure className={s.chart}>
      <figcaption className={s.caption}>
        <span className={s.title}>Matches over time</span>
        <span className={s.sub}>
          One bar per {per}. Every match in the {n(scanned)} segment{scanned === 1 ? '' : 's'} read is counted; a skipped segment can't hold one.
        </span>
      </figcaption>

      <div className={s.frame}>
        <span className={`${s.yTop} mono`} aria-hidden="true">{compact.format(top)}</span>
        <span className={`${s.yZero} mono`} aria-hidden="true">0</span>
        <div
          ref={plot}
          className={s.plot}
          role="img"
          tabIndex={0}
          aria-label={`Matches over time, ${n(h.counts.length)} bars of ${per} from ${stamp(h.start_ms, h.bucket_ms)} to ${stamp(end, h.bucket_ms)} UTC; the busiest has ${n(max)}. Use the arrow keys for each bar's count.`}
          onPointerLeave={() => setActive(null)}
          onBlur={() => setActive(null)}
          onKeyDown={(e) => {
            const last = h.counts.length - 1;
            const move: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1, Home: -Infinity, End: Infinity };
            if (e.key in move) {
              e.preventDefault();
              setActive((a) => Math.min(Math.max((a ?? (move[e.key] > 0 ? -1 : last + 1)) + move[e.key], 0), last));
            } else if (e.key === 'Escape') setActive(null);
          }}
        >
          {h.counts.map((c, i) => (
            <span key={i} className={s.slot} data-active={active === i || undefined} onPointerEnter={() => setActive(i)}>
              {c > 0 && <i className={s.bar} style={{ height: `max(2px, ${(c / top) * 100}%)` }} />}
            </span>
          ))}
          {active !== null && (
            <span ref={tip} className={s.tip} style={{ left: tipX }} aria-live="polite">
              <span>
                <b className="mono">{n(h.counts[active])}</b> {h.counts[active] === 1 ? 'match' : 'matches'}
              </span>
              <span className="mono">{span(h.start_ms + active * h.bucket_ms, h.bucket_ms)} UTC</span>
            </span>
          )}
        </div>
      </div>
      <p className={`${s.axis} mono`} aria-hidden="true">
        <span>{stamp(axisFrom, h.bucket_ms)}</span>
        <span>{stamp(axisTo, h.bucket_ms)}</span>
      </p>

      <details className={s.table}>
        <summary>
          <ChevronIcon className={s.chev} />
          Counts as a table ({n(nonZero)} of {n(h.counts.length)} bars have matches)
        </summary>
        <table role="table">
          <thead role="rowgroup">
            <tr role="row">
              <th role="columnheader">From – to (UTC)</th>
              <th role="columnheader">Matches</th>
            </tr>
          </thead>
          <tbody role="rowgroup">
            {h.counts.map((c, i) =>
              c > 0 ? (
                <tr role="row" key={i}>
                  <td role="cell" className="mono">{span(h.start_ms + i * h.bucket_ms, h.bucket_ms)}</td>
                  <td role="cell" className="mono">{n(c)}</td>
                </tr>
              ) : null,
            )}
          </tbody>
        </table>
      </details>
    </figure>
  );
}
