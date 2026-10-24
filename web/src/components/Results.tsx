import { useMemo, useState } from 'react';
import type { SearchResult } from '../lib/api';
import { n, shortId } from '../lib/format';
import { LIMITS } from '../lib/query';
import { LogSkeleton, LogViewer } from './LogViewer';
import { MatchChart } from './MatchChart';
import s from './Results.module.css';

interface Props {
  result: SearchResult | null; // null while the first search runs
  stale: boolean;
  limit: number;
  terms: Set<string>;
  tags: Set<string>;
  extent: [number | null, number | null]; // the query's range within the stored data, ms
  segment: string | null; // show only the lines from this read segment
  onSegment: (id: string | null) => void;
  onLimit: (limit: number) => void;
  onTag: (key: string, value: string) => void;
}

// A per-browser preference; storage can be missing or refuse (private
// windows), and the page works the same without it.
function storedWrap(): boolean {
  try {
    return localStorage.getItem('strata-wrap') !== 'off';
  } catch {
    return true;
  }
}

// How many lines match in all, how they spread over time, and the earliest of
// them. The total is the histogram's sum: every match, not only those shown.
export function Results({ result, stale, limit, terms, tags, extent, segment, onSegment, onLimit, onTag }: Props) {
  const hits = result?.hits ?? [];
  // A segment picked in the strip narrows the lines shown, on the page: each
  // line already says which segment it came from.
  const from = segment ? result?.segments?.find((g) => g.id === segment) : undefined;
  const lines = useMemo(() => (segment ? hits.filter((h) => h.segment_id === segment) : hits), [hits, segment]);
  const total = result?.histogram ? result.histogram.counts.reduce((a, b) => a + b, 0) : hits.length;
  const m = result?.metrics;
  // Wrapped by default: in one line per row, a matched word late in a long
  // line would sit behind the ellipsis.
  const [wrap, setWrap] = useState(storedWrap);
  // A limit from a link or an example that isn't one of the usual choices
  // still shows as selected.
  const choices = LIMITS.includes(limit) ? LIMITS : [...LIMITS, limit].sort((a, b) => a - b);

  if (result && hits.length === 0) {
    return (
      <section className={s.panel} data-stale={stale || undefined} aria-label="Matching lines">
        <p className={s.none} role="status">
          <b>No line matched.</b>{' '}
          {m && m.segments_considered > 0
            ? `The search still ran: ${n(m.segments_scanned)} of ${n(m.segments_considered)} segments were read. A line must contain every word, so try fewer words or a wider time range.`
            : 'Nothing is stored in this time range.'}
        </p>
      </section>
    );
  }

  return (
    <section className={s.panel} data-stale={stale || undefined} aria-labelledby="matches-title" aria-busy={!result || undefined}>
      <header className={s.head}>
        <h2 id="matches-title" className={s.count} aria-live="polite">
          {result ? (
            <>
              <span className="mono">{n(total)}</span> line{total === 1 ? '' : 's'} match
              {total > hits.length && <span className={s.shown}>. Showing the earliest {n(hits.length)}.</span>}
            </>
          ) : (
            'Searching…'
          )}
        </h2>
        <div className={s.controls}>
        <button
          type="button"
          className={s.wrap}
          aria-pressed={wrap}
          onClick={() => {
            setWrap(!wrap);
            try {
              localStorage.setItem('strata-wrap', wrap ? 'off' : 'on');
            } catch {
              // not remembered; fine
            }
          }}
        >
          <span className={s.track} aria-hidden="true" />
          Wrap lines
        </button>
        <label className={s.limit}>
          <span>Show</span>
          <select value={limit} onChange={(e) => onLimit(Number(e.target.value))}>
            {choices.map((v) => (
              <option key={v} value={v}>
                {n(v)} lines
              </option>
            ))}
          </select>
        </label>
        </div>
      </header>
      {result?.histogram && m && <MatchChart histogram={result.histogram} scanned={m.segments_scanned} extent={extent} />}
      {segment && (
        <div className={s.filter} role="status">
          <p>
            {lines.length > 0 ? (
              <>
                {lines.length === hits.length ? 'All' : 'Only the'} <span className="mono">{n(lines.length)}</span> of these{' '}
                {n(hits.length)} lines {lines.length === hits.length ? 'came' : 'that came'} from segment <span className="mono">{shortId(segment)}</span>.
              </>
            ) : (
              <>
                None of these {n(hits.length)} lines came from segment <span className="mono">{shortId(segment)}</span>
                {from && (
                  <>
                    {' '}
                    (<span className="mono">{from.first}</span> to <span className="mono">{from.last}</span>)
                  </>
                )}
                : the earliest matches were in other segments. Show more lines, or narrow the time range to its span.
              </>
            )}
          </p>
          <button type="button" className={s.clear} onClick={() => onSegment(null)}>
            Show all lines
          </button>
        </div>
      )}
      {result ? lines.length > 0 && <LogViewer wrap={wrap} hits={lines} terms={terms} tags={tags} onTag={onTag} /> : <LogSkeleton />}
    </section>
  );
}
