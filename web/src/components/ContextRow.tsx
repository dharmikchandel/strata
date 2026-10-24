import type { Corpus } from '../lib/api';
import { formatBytes, n } from '../lib/format';
import s from './ContextRow.module.css';

interface Props {
  name: string;
  corpus: Corpus | null;
  live: boolean;
  rate: number | null; // lines stored per second, measured while live
  onLive: () => void;
}

// What is being searched, and how much of it there is, kept current by the
// overview refresh (every 2 s while live). At the far end, how fresh the data
// is and the switch that follows it as it arrives.
export function ContextRow({ name, corpus, live, rate, onLive }: Props) {
  return (
    <section className={s.row} aria-labelledby="corpus-name">
      <h1 id="corpus-name" className={s.name}>{name}</h1>
      {corpus && (
        <>
          <p className={`${s.facts} mono`}>
            <span><b>{n(corpus.entries)}</b> lines</span>
            <span><b>{n(corpus.segments)}</b> segment{corpus.segments === 1 ? '' : 's'}</span>
            <span><b>{formatBytes(corpus.stored_bytes)}</b> stored</span>
            <span className={s.range}>{corpus.first} → {corpus.last} UTC</span>
          </p>
          <div className={s.fresh}>
            {live && rate !== null && (
              <span className={s.rate} title="Lines stored per second over the last 30 seconds, counted as their segments are sealed">
                <b className="mono">{n(Math.round(rate))}</b> lines/s
              </span>
            )}
            <span>
              Newest line <time className="mono" dateTime={corpus.last.replace(' ', 'T') + 'Z'}>{corpus.last}</time> UTC
            </span>
            <button type="button" className={s.live} aria-pressed={live} onClick={onLive}>
              <span className={s.dot} aria-hidden="true" />
              Live
            </button>
          </div>
        </>
      )}
    </section>
  );
}
