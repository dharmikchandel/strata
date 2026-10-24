import type { SearchResult } from '../lib/api';
import { formatBytes, plural } from '../lib/format';
import { ReadList } from './ReadList';
import { Strip } from './Strip';
import { ChevronIcon } from './icons';
import s from './Readout.module.css';

// The terms in the readout, each with a small glyph drawn the way that kind
// of segment looks in the strip, and what it costs a search.
const GLOSSARY: [string, string, string, string][] = [
  ['segment', 'Segment', 'one file in object storage', 'An immutable file holding a batch of log lines, with its own index and time range. Every segment is one bar in the strip above, oldest on the left.'],
  ['time', 'Skipped by time range', 'cost: one manifest lookup, no download', "The segment's earliest and latest lines fall outside the range you asked for, so it was ruled out from a small catalogue without being opened."],
  ['bloom', 'Skipped by bloom filter', 'cost: an in-memory check, no download', 'A compact summary of every word in the segment said a word you searched for is definitely not in it. It can wrongly say "maybe" (costing one wasted read), never wrongly say "no".'],
  ['scanned', 'Read', 'cost: a download of the whole segment', 'Downloaded and searched, the only step that touches storage. A tall bar in the strip; the fewer there are, the less the search read.'],
];

// What one search did: a sentence, then the segments themselves, then the
// detail for anyone who wants it.
export function Readout({ result, stale }: { result: SearchResult; stale: boolean }) {
  const m = result.metrics;
  if (!m || m.segments_considered === 0) return null;
  const total = m.segments_considered;
  const scanned = m.segments_scanned;
  const skipped = m.skipped_by_time + m.skipped_by_bloom;
  const bytes = formatBytes(m.bytes_read);

  let lead = '';
  let read = '';
  if (total === 1) {
    lead = scanned ? '' : 'Skipped the only segment. ';
    read = scanned ? `Read the only segment (${bytes}).` : 'Read nothing.';
  } else if (scanned === 0) {
    lead = `Skipped all ${plural(total, 'segment')}. `;
    read = 'Read nothing.';
  } else if (scanned === total) {
    read = `Read all ${plural(total, 'segment')} (${bytes}).`;
  } else {
    lead = `Skipped ${skipped.toLocaleString('en-US')} of ${plural(total, 'segment')}. `;
    read = `Read ${scanned.toLocaleString('en-US')} (${bytes}).`;
  }

  const legend: [string, string, number][] = [
    ['time', 'Skipped by time range', m.skipped_by_time],
    ['bloom', 'Skipped by bloom filter', m.skipped_by_bloom],
    ['scanned', 'Read', scanned],
  ];

  return (
    <section className={s.panel} data-stale={stale || undefined} aria-labelledby="readout-title">
      <div className={s.head}>
        <h2 id="readout-title" className={s.headline} aria-live="polite">
          {lead}
          <span className={s.read} data-none={scanned === 0 || undefined}>{read}</span>
        </h2>
      </div>

      <Strip result={result} metrics={m} />

      <div className={s.keyRow}>
        <ul className={s.legend}>
          {legend.map(([kind, text, v]) => (
            <li key={kind} data-zero={!v || undefined}>
              <span className={`${s.swatch} ${s[kind]}`} aria-hidden="true" />
              {text}
              <b className="mono">{v.toLocaleString('en-US')}</b>
            </li>
          ))}
        </ul>
        <p className={`${s.timing} mono`}>
          server {(m.server_micros / 1000).toFixed(1)} ms · round trip {result.round_trip_ms.toFixed(1)} ms
          {m.retries ? ` · restarted ${m.retries}×` : ''}
        </p>
      </div>

      <div className={s.more}>
        <ReadList segments={result.segments ?? []} />
        <details className={s.explain}>
          <summary>
            <ChevronIcon className={s.chev} />
            What do these mean?
          </summary>
          <dl className={s.glossary}>
            {GLOSSARY.map(([kind, term, cost, text]) => (
              <div key={kind} className={s.entry}>
                <span className={s.glyph} data-kind={kind} aria-hidden="true">
                  <i />
                  <i />
                  <i />
                  <i />
                  <i />
                </span>
                <dt>{term}</dt>
                <dd className={`${s.cost} mono`}>{cost}</dd>
                <dd className={s.text}>{text}</dd>
              </div>
            ))}
          </dl>
        </details>
      </div>
    </section>
  );
}
