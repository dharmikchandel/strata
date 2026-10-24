import { useId, useState, type ReactNode } from 'react';
import s from './PairedBars.module.css';

export interface Row {
  label: string; // which configuration
  sub: string; // a short fact about it, e.g. "17 segments"
  left: number;
  right: number;
  detail: ReactNode; // shown when the row is pointed at or focused
}

export interface Group {
  title: string;
  note?: string;
  rows: Row[];
}

interface Column {
  title: string;
  format: (v: number) => string;
  kind: 'read' | 'neutral'; // read: data fetched from storage (orange); neutral: anything else
}

interface Props {
  label: string; // what the whole chart shows, for screen readers
  groups: Group[];
  left: Column;
  right: Column;
}

// The smallest round number at or above v, for each column's scale.
function niceCeil(v: number): number {
  if (v <= 0) return 1;
  const p = 10 ** Math.floor(Math.log10(v));
  return [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10].map((m) => m * p).find((x) => x >= v)!;
}

// Two measures of the same rows, side by side: each has its own column and
// its own scale (never two scales on one axis), and the rows line up across
// them, so "this one read more and took longer" is one glance along a row.
// A zero draws no bar at all (a sliver of orange would say "read").
// Every bar carries its value; pointing at a row (or tabbing to it) adds the
// rest of its figures underneath.
export function PairedBars({ label, groups, left, right }: Props) {
  const [active, setActive] = useState<string | null>(null);
  const id = useId();
  const all = groups.flatMap((g) => g.rows);
  const maxL = niceCeil(Math.max(...all.map((r) => r.left)));
  const maxR = niceCeil(Math.max(...all.map((r) => r.right)));

  return (
    <figure className={s.chart} aria-label={label}>
      <div className={s.head} aria-hidden="true">
        <span />
        <span className={s.colTitle}>
          {left.title}
          <span className="mono">0 – {left.format(maxL)}</span>
        </span>
        <span className={s.colTitle}>
          {right.title}
          <span className="mono">0 – {right.format(maxR)}</span>
        </span>
      </div>
      {groups.map((g, gi) => (
        <div key={g.title} className={s.group} role="group" aria-label={g.title}>
          <p className={s.groupTitle}>
            {g.title}
            {g.note && <span>{g.note}</span>}
          </p>
          {g.rows.map((r, ri) => {
            const key = `${gi}-${ri}`;
            return (
              <div
                key={key}
                className={s.row}
                tabIndex={0}
                data-active={active === key || undefined}
                aria-describedby={active === key ? `${id}-${key}` : undefined}
                onPointerEnter={() => setActive(key)}
                onPointerLeave={() => setActive(null)}
                onFocus={() => setActive(key)}
                onBlur={() => setActive(null)}
              >
                <span className={s.label}>
                  {r.label}
                  <span className="mono">{r.sub}</span>
                </span>
                <span className={s.cell}>
                  <span className={s.caption}>{left.title}</span>
                  <span className={s.track}>
                    <span className={s.area}>
                      {r.left > 0 && <i data-kind={left.kind} style={{ width: `${Math.max((r.left / maxL) * 100, 0.6)}%` }} />}
                    </span>
                    <b className="mono">{left.format(r.left)}</b>
                  </span>
                </span>
                <span className={s.cell}>
                  <span className={s.caption}>{right.title}</span>
                  <span className={s.track}>
                    <span className={s.area}>
                      {r.right > 0 && <i data-kind={right.kind} style={{ width: `${Math.max((r.right / maxR) * 100, 0.6)}%` }} />}
                    </span>
                    <b className="mono">{right.format(r.right)}</b>
                  </span>
                </span>
                {active === key && (
                  <span id={`${id}-${key}`} className={`${s.detail} mono`}>
                    {r.detail}
                  </span>
                )}
              </div>
            );
          })}
        </div>
      ))}
    </figure>
  );
}
