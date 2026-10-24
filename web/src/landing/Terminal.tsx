import { Fragment } from 'react';
import type { Example, SearchResult } from '../lib/api';
import { cliCommand, cliOutput } from '../lib/cli';
import s from './Terminal.module.css';

// A run captured from the README, shown only when the page can't reach a
// server (for example, the page hosted on its own). Labelled as recorded.
const RECORDED = {
  command: 'stratactl search -text hangtest -limit 3',
  lines: [
    '2005-06-20T13:44:57.111650Z  2005.06.20 R10-M0-N0-I:J18-U11 2005-06-20-13.44.57.111650 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...',
    '2005-06-20T13:44:57.139680Z  2005.06.20 R10-M0-N0-I:J18-U01 2005-06-20-13.44.57.139680 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...',
    '2005-06-20T13:44:57.166392Z  2005.06.20 R10-M1-NC-I:J18-U11 2005-06-20-13.44.57.166392 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...',
    '... more lines match; showing the earliest 3 (raise -limit)',
    '',
    'segments  considered 73 | skipped by time 0 | skipped by bloom filter 71 | scanned 2',
    '          [----------------------------------------------------------#]  . time  - bloom  # scanned',
    'read      26.4 MiB from storage | server time 133.45ms | round trip 139.2ms',
  ],
};

export const TERMINAL_LIMIT = 3;

// Log lines may end in an ellipsis, as the README shows them; the summary
// lines wrap instead, because their numbers are the point. The 60-column bar
// is dropped when the panel is narrower than it (the strip below draws it).
function lineKind(l: string): string {
  if (l.startsWith('segments') || l.startsWith('read ') || l.startsWith('          (')) return s.stat;
  if (l.startsWith('          [')) return `${s.stat} ${s.bar}`;
  return s.log;
}

// The bar and its key each stay whole; if the panel is too narrow for both on
// one line, the key moves under the bar.
function BarLine({ line }: { line: string }) {
  const end = line.indexOf(']') + 1;
  return (
    <>
      <span className={s.unit}>{line.slice(0, end)}</span>
      {'  '}
      <span className={s.unit}>{line.slice(end).trimStart()}</span>
    </>
  );
}

type Props =
  | { state: 'loading'; example: Example }
  | { state: 'live'; example: Example; result: SearchResult }
  | { state: 'recorded' };

export function Terminal(p: Props) {
  const command = p.state === 'recorded' ? RECORDED.command : cliCommand(p.example, TERMINAL_LIMIT);
  const lines = p.state === 'live' ? cliOutput(p.result) : p.state === 'recorded' ? RECORDED.lines : [];
  return (
    <figure className={s.term} aria-label="A search run with the stratactl command-line client">
      <figcaption className={s.head}>
        <span className={s.title}>stratactl</span>
        <span className={s.badge} data-state={p.state}>
          {p.state === 'live' ? 'live' : p.state === 'loading' ? 'running' : 'recorded run'}
        </span>
      </figcaption>
      <pre className={s.body}>
        <code>
          <span className={`${s.line} ${s.command}`}>
            <span className={s.prompt}>$ </span>
            {/* each flag with its value stays on one line when the command wraps */}
            {command.split(/ (?=-)/).map((part, i) => (
              <Fragment key={i}>
                {i > 0 ? ' ' : ''}
                <span className={s.unit}>{part}</span>
              </Fragment>
            ))}
          </span>
          {p.state === 'loading' && <span className={`${s.line} ${s.dim}`}>searching…</span>}
          {lines.map((l, i) => (
            <span key={i} className={`${s.line} ${lineKind(l)}`}>
              {l === '' ? '\u00a0' : l.startsWith('          [') ? <BarLine line={l} /> : l}
            </span>
          ))}
          {p.state !== 'loading' && (
            <span className={s.line}>
              <span className={s.prompt}>$ </span>
              <span className={s.caret} aria-hidden="true" />
            </span>
          )}
        </code>
      </pre>
    </figure>
  );
}
