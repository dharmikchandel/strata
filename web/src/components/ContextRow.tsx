import type { Corpus } from '../lib/api';
import { formatBytes, n } from '../lib/format';
import s from './ContextRow.module.css';

interface Props {
  name: string;
  corpus: Corpus | null;
}

// What is being searched, and how much of it there is, kept current by the
// overview refresh. The newest line says how fresh the data is.
export function ContextRow({ name, corpus }: Props) {
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
          <p className={s.fresh}>
            <span className={s.dot} aria-hidden="true" />
            Newest line <time className="mono" dateTime={corpus.last.replace(' ', 'T') + 'Z'}>{corpus.last}</time> UTC
          </p>
        </>
      )}
    </section>
  );
}
