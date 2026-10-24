import { useLayoutEffect, useRef, useState } from 'react';
import type { Metrics, SearchResult, SegmentKind } from '../lib/api';
import { formatBytes, shortId } from '../lib/format';
import s from './Strip.module.css';

const KIND_TEXT: Record<SegmentKind, string> = {
  time: 'skipped by time range',
  bloom: 'skipped by bloom filter',
  scanned: 'read',
};

// Every segment as one bar, oldest on the left: a segment that was read stands
// tall, a skipped one stays low, so a good search is a flat line with a few
// spikes. If the server judged there were too many to list, the three totals
// are drawn as proportional blocks instead.
export function Strip({ result, metrics }: { result: SearchResult; metrics: Metrics }) {
  const segs = result.segments ?? [];
  const total = metrics.segments_considered;
  const ref = useRef<HTMLDivElement>(null);
  const [dense, setDense] = useState(false);
  const [risen, setRisen] = useState(false);

  const label =
    `${total.toLocaleString('en-US')} segments: ${metrics.skipped_by_time} skipped by time range, ` +
    `${metrics.skipped_by_bloom} skipped by bloom filter, ${metrics.segments_scanned} read.`;

  // Bars are at least 1px wide with a 1px gap; when that doesn't fit (hundreds
  // of segments, or a phone), drop the gaps so nothing overflows. Then let the
  // read bars rise once, on the next frame, so the transition has a start.
  useLayoutEffect(() => {
    const el = ref.current;
    if (el) setDense(segs.length * 2 > el.clientWidth);
    setRisen(false);
    let inner = 0;
    const outer = requestAnimationFrame(() => {
      inner = requestAnimationFrame(() => setRisen(true));
    });
    return () => {
      cancelAnimationFrame(outer);
      cancelAnimationFrame(inner);
    };
  }, [result, segs.length]);

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
  let order = 0;
  return (
    <div className={s.box} style={{ '--n': segs.length } as React.CSSProperties}>
      <div
        ref={ref}
        className={[s.strip, dense && s.dense, !anyRead && s.flat, s.rise, risen && s.go].filter(Boolean).join(' ')}
        role="img"
        aria-label={label}
      >
        {segs.map((g) => {
          const read = g.kind === 'scanned';
          const delay = read ? Math.min(order++ * 12, 400) : 0; // a short, bounded wave
          return (
            <i
              key={g.id}
              className={s[g.kind]}
              style={read ? { transitionDelay: delay + 'ms' } : undefined}
              title={
                `${shortId(g.id)} · ${g.first} to ${g.last} UTC · ${KIND_TEXT[g.kind]}` +
                (read ? ` · ${formatBytes(g.bytes_read)} · ${g.hits} line${g.hits === 1 ? '' : 's'} shown` : '')
              }
            />
          );
        })}
      </div>
      <p className={`${s.axis} mono`}>
        <span>{segs[0].first.slice(0, 10)}</span>
        <span>{segs[segs.length - 1].last.slice(0, 10)}</span>
      </p>
    </div>
  );
}
