import type { SegmentOutcome } from '../lib/api';
import { formatBytes, shortId } from '../lib/format';
import { ChevronIcon } from './icons';
import s from './Readout.module.css';
import t from './ReadList.module.css';

const SHOW = 12;

// Which segments were read, and how many of the lines shown each supplied:
// the answer to "I got 20 lines, why did it read 171 segments?".
export function ReadList({ segments }: { segments: SegmentOutcome[] }) {
  const read = segments.filter((g) => g.kind === 'scanned');
  if (read.length === 0) return null;
  return (
    // keyed on the list so a new search starts it closed
    <details className={s.explain} key={read.map((g) => g.id).join()}>
      <summary>
        <ChevronIcon className={s.chev} />
        Which segments were read ({read.length.toLocaleString('en-US')})
      </summary>
      <table className={t.table} role="table">
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader">Segment</th>
            <th role="columnheader">Covers (UTC)</th>
            <th role="columnheader">Size</th>
            <th role="columnheader">Lines shown</th>
          </tr>
        </thead>
        <tbody role="rowgroup">
          {read.slice(0, SHOW).map((g) => (
            <tr role="row" key={g.id}>
              <td role="cell" className={`${t.id} mono`} title={g.id}>{shortId(g.id)}</td>
              <td role="cell" className={`${t.covers} mono`} data-label="covers">{g.first} to {g.last}</td>
              <td role="cell" className="mono" data-label="size">{formatBytes(g.bytes_read)}</td>
              <td role="cell" className="mono" data-label="lines shown">{g.hits}</td>
            </tr>
          ))}
          {read.length > SHOW && (
            <tr role="row" className={t.more}>
              <td role="cell" colSpan={4}>and {(read.length - SHOW).toLocaleString('en-US')} more</td>
            </tr>
          )}
        </tbody>
      </table>
    </details>
  );
}
