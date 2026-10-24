import type { SegmentOutcome } from '../lib/api';
import { formatBytes, n, shortId } from '../lib/format';
import { ChevronIcon } from './icons';
import s from './Readout.module.css';
import t from './ReadList.module.css';

const SHOW = 12;

interface Props {
  segments: SegmentOutcome[];
  hot: string | null; // pointed at here or in the strip
  onHot: (id: string | null) => void;
  picked: string | null;
  onPick?: (id: string | null) => void; // without it, rows only point at their bars
  live?: boolean; // the live view's own searches: their lines aren't the ones on screen
}

// Which segments were read, and how many of the lines shown each supplied:
// the answer to "I got 20 lines, why did it read 171 segments?". Pointing at a
// row lights its bar in the strip; each row can show only that segment's lines.
export function ReadList({ segments, hot, onHot, picked, onPick, live = false }: Props) {
  const read = segments.filter((g) => g.kind === 'scanned');
  if (read.length === 0) return null;
  return (
    // keyed on the list so a new search starts it closed
    <details className={s.explain} key={read.map((g) => g.id).join()}>
      <summary>
        <ChevronIcon className={s.chev} />
        Which segments were read ({n(read.length)})
      </summary>
      <table className={t.table} role="table" onPointerLeave={() => onHot(null)}>
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader">Segment</th>
            <th role="columnheader">Covers (UTC)</th>
            <th role="columnheader">Size</th>
            {!live && <th role="columnheader">Lines shown</th>}
            {onPick && (
              <th role="columnheader">
                <span className="sr-only">Filter</span>
              </th>
            )}
          </tr>
        </thead>
        <tbody role="rowgroup">
          {read.slice(0, SHOW).map((g) => (
            <tr role="row" key={g.id} data-hot={hot === g.id || undefined} onPointerEnter={() => onHot(g.id)}>
              <td role="cell" className={`${t.id} mono`} title={g.id}>{shortId(g.id)}</td>
              <td role="cell" className={`${t.covers} mono`} data-label="covers">{g.first} to {g.last}</td>
              <td role="cell" className="mono" data-label="size">{formatBytes(g.bytes_read)}</td>
              {!live && <td role="cell" className="mono" data-label="lines shown">{n(g.hits)}</td>}
              {onPick && (
              <td role="cell" className={t.act}>
                <button
                  type="button"
                  className={t.pick}
                  aria-pressed={picked === g.id}
                  onClick={() => onPick(picked === g.id ? null : g.id)}
                  onFocus={() => onHot(g.id)}
                  onBlur={() => onHot(null)}
                >
                  {picked === g.id ? 'Showing its lines' : 'Show its lines'}
                </button>
              </td>
              )}
            </tr>
          ))}
          {read.length > SHOW && (
            <tr role="row" className={t.more}>
              <td role="cell" colSpan={3 + (live ? 0 : 1) + (onPick ? 1 : 0)}>and {n(read.length - SHOW)} more (point at them in the strip)</td>
            </tr>
          )}
        </tbody>
      </table>
    </details>
  );
}
