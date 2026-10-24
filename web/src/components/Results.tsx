import type { Hit } from '../lib/api';
import s from './Results.module.css';

// The matching lines. Log text is untrusted: React renders it as text.
// The explicit roles keep this a table for assistive tech on phones, where the
// rows are restyled as stacked entries.
export function Results({ hits, stale }: { hits: Hit[]; stale: boolean }) {
  if (hits.length === 0) return null;
  return (
    <div className={s.wrap} data-stale={stale || undefined}>
      <table className={s.table} role="table">
        <thead role="rowgroup">
          <tr role="row">
            <th role="columnheader" className={s.tsHead}>Time (UTC)</th>
            <th role="columnheader">Message</th>
            <th role="columnheader">Tags</th>
          </tr>
        </thead>
        <tbody role="rowgroup">
          {hits.map((h, i) => (
            <tr role="row" key={i}>
              <td role="cell" className={s.ts}>{h.time}</td>
              <td role="cell" className={s.msg}>{h.message}</td>
              <td role="cell" className={s.tags}>
                {Object.entries(h.tags ?? {}).map(([k, v]) => (
                  <span key={k} className={s.tag}>{k}={v}</span>
                ))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
