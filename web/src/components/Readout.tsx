import { useMemo, useState } from 'react';
import type { SearchResult, SegmentKind } from '../lib/api';
import { formatBytes, n, plural } from '../lib/format';
import { avoided, plan } from '../lib/stages';
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

interface Props {
  result: SearchResult;
  stale: boolean;
  live: boolean; // results keep coming: no replay per result, and no picking
  stored: number | null; // the overview's stored bytes, used when segments aren't listed
  picked: string | null; // the read segment whose lines are shown
  onPick?: (id: string | null) => void;
}

let plays = 0; // numbers each result, so the legend's counts arrive again

// What one search did: a sentence, what share of the stored data it never
// downloaded, then the segments themselves, then the detail for anyone who
// wants it.
export function Readout({ result, stale, live, stored, picked, onPick }: Props) {
  const [hot, setHot] = useState<string | null>(null); // pointed at in the strip or the list
  // Live, a result arrives every few seconds: the legend keeps its place
  // instead of replaying each time.
  const play = useMemo(() => (live ? 0 : ++plays), [result, live]);
  const segs = result.segments ?? [];
  const { ends } = useMemo(() => plan(segs.map((g) => g.kind)), [segs]);
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

  // Bytes per outcome come from the segments' own sizes, from the same
  // manifest snapshot as the search. Without the list, only the total is
  // known (the overview's, a few seconds older at most).
  const bytesBy: Record<SegmentKind, number> = { time: 0, bloom: 0, scanned: 0 };
  for (const g of segs) bytesBy[g.kind] += g.kind === 'scanned' ? g.bytes_read : g.size;
  const whole = segs.length ? bytesBy.time + bytesBy.bloom + bytesBy.scanned : (stored ?? 0);
  const share = avoided(m.bytes_read, whole);

  const legend: [SegmentKind, string, number][] = [
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
        {share && (
          <p className={s.saved}>
            {scanned === 0 ? (
              <>
                None of the <span className="mono">{formatBytes(whole)}</span> stored was downloaded.
              </>
            ) : scanned === total ? (
              <>
                All <span className="mono">{formatBytes(whole)}</span> stored was downloaded: every segment could hold a match.
              </>
            ) : (
              <>
                {live && 'The live view searches every 2 s for new lines; the latest search never downloaded '}
                <b className="mono">{share}</b> of the <span className="mono">{formatBytes(whole)}</span> stored
                {live ? '.' : ' was never downloaded.'}
              </>
            )}
          </p>
        )}
      </div>

      <Strip result={result} metrics={m} live={live} hot={hot} picked={picked} onHot={setHot} onPick={onPick} />

      <div className={s.keyRow}>
        {/* Each count arrives when the strip has played its stage. */}
        <ul className={s.legend} key={play}>
          {legend.map(([kind, text, v]) => (
            <li key={kind} data-zero={!v || undefined}>
              <span className={`${s.swatch} ${s[kind]}`} aria-hidden="true" />
              {text}
              <span className={s.figures} style={{ '--d': ends[kind] + 'ms' } as React.CSSProperties}>
                <b className="mono">{n(v)}</b>
                {v > 0 && segs.length > 0 && <span className="mono">({formatBytes(bytesBy[kind])})</span>}
              </span>
            </li>
          ))}
        </ul>
        <p className={`${s.timing} mono`}>
          server {(m.server_micros / 1000).toFixed(1)} ms · round trip {result.round_trip_ms.toFixed(1)} ms
          {m.retries ? ` · restarted ${m.retries}×` : ''}
        </p>
      </div>

      <div className={s.more}>
        <ReadList segments={segs} hot={hot} onHot={setHot} picked={picked} onPick={onPick} live={live} />
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
