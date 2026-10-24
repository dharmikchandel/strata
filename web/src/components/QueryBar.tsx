import { forwardRef } from 'react';
import type { Corpus } from '../lib/api';
import { parse, type Query, type Range } from '../lib/query';
import { terms } from '../lib/tokenize';
import { SearchIcon } from './icons';
import { TimeRange } from './TimeRange';
import s from './QueryBar.module.css';

interface Props {
  query: Query;
  corpus: Corpus | null;
  busy: boolean;
  invalid: boolean; // the last check found a problem in the box itself
  onText: (q: string) => void;
  onRange: (r: Range) => void;
  onSubmit: () => void;
}

// Inputs for log terms: no capitalising, correcting or spell-checking.
const plain = { autoComplete: 'off', autoCapitalize: 'off', autoCorrect: 'off', spellCheck: false } as const;

// One box for words and tags, the time control, and the action, as a single
// instrument. Under it, what the box will search for, split the way the
// server splits it, so "LAPACK_81117d" visibly becomes two terms.
export const QueryBar = forwardRef<HTMLInputElement, Props>(function QueryBar(
  { query, corpus, busy, invalid, onText, onRange, onSubmit },
  inputRef,
) {
  const { words, tags } = parse(query.q);
  const ts = terms(words);

  return (
    <form
      className={s.form}
      role="search"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <div className={s.bar}>
        <label className={s.box}>
          <SearchIcon className={s.icon} />
          <span className="sr-only">Search words and tags</span>
          <input
            ref={inputRef}
            name="q"
            value={query.q}
            onChange={(e) => onText(e.target.value)}
            onKeyDown={(e) => {
              // Esc empties the box; a second Esc (already empty) leaves it.
              if (e.key === 'Escape' && query.q) {
                e.preventDefault();
                onText('');
              } else if (e.key === 'Escape') {
                e.currentTarget.blur();
              }
            }}
            placeholder="e.g. parity error level=FATAL"
            enterKeyHint="search"
            aria-describedby={invalid ? 'search-error query-help' : 'query-help'}
            aria-invalid={invalid || undefined}
            {...plain}
          />
          <kbd className={s.slash} aria-hidden="true">/</kbd>
        </label>
        <TimeRange range={query.range} corpus={corpus} onPick={onRange} />
        <button type="submit" className={s.submit} disabled={busy}>
          {busy ? 'Searching…' : 'Search'}
        </button>
      </div>

      <p id="query-help" className={s.help}>
        {ts.length === 0 && tags.length === 0 ? (
          words ? (
            <>These words have no letters or digits to search for, so every line in the time range matches.</>
          ) : (
            <>
              A line matches when it contains every word, in any order. Add <code className="mono">key=value</code> to filter by a tag.
              <span className={s.keys}> Press <kbd className="mono">/</kbd> to search from anywhere.</span>
            </>
          )
        ) : (
          <>
            {ts.length > 0 && (
              <>
                Lines containing{' '}
                {ts.map((t, i) => (
                  <span key={t}>
                    {i > 0 && (i === ts.length - 1 ? ' and ' : ', ')}
                    <code className={`${s.term} mono`}>{t}</code>
                  </span>
                ))}
              </>
            )}
            {tags.length > 0 && (
              <>
                {ts.length > 0 ? ', tagged ' : 'Lines tagged '}
                {tags.map(([k, v], i) => (
                  <span key={k + '=' + v}>
                    {i > 0 && (i === tags.length - 1 ? ' and ' : ', ')}
                    <code className={`${s.term} mono`}>
                      {k}={v}
                    </code>
                  </span>
                ))}
              </>
            )}
            .
          </>
        )}
      </p>
    </form>
  );
});
