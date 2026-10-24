import { forwardRef, useState } from 'react';
import type { Corpus, TagValues } from '../lib/api';
import { parse, type Query, type Range } from '../lib/query';
import { accept, suggest } from '../lib/suggest';
import { terms } from '../lib/tokenize';
import { SearchIcon } from './icons';
import { TimeRange } from './TimeRange';
import s from './QueryBar.module.css';

interface Props {
  query: Query;
  corpus: Corpus | null;
  tags: TagValues[]; // stored tag values, for suggestions
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
  { query, corpus, tags: stored, busy, invalid, onText, onRange, onSubmit },
  inputRef,
) {
  const { words, tags } = parse(query.q);
  const ts = terms(words);
  // Tag suggestions: a list under the box for the word at the caret, opened
  // by typing and closed by Esc, a pick, or leaving the box. Arrow keys move
  // through it; Enter or Tab takes the highlighted one (with none highlighted,
  // Enter searches as usual).
  const [caret, setCaret] = useState<number | null>(null); // null: the box isn't focused
  const [open, setOpen] = useState(false);
  const [pick, setPick] = useState(-1);
  const sugg = caret === null ? null : suggest(query.q, caret, stored);
  const shown = open && sugg && sugg.items.length > 0 ? sugg : null;
  const input = () => (inputRef && typeof inputRef !== 'function' ? inputRef.current : null);
  const take = (i: number) => {
    if (!shown) return;
    const next = accept(query.q, shown, shown.items[i]);
    onText(next.text);
    setCaret(next.caret);
    setPick(-1);
    setOpen(!shown.items[i].complete); // after "level=", offer its values
    requestAnimationFrame(() => input()?.setSelectionRange(next.caret, next.caret));
  };
  const track = (el: HTMLInputElement) => setCaret(el.selectionStart ?? el.value.length);

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
      <div className={s.anchor}>
      <div className={s.bar}>
        <label className={s.box}>
          <SearchIcon className={s.icon} />
          <span className="sr-only">Search words and tags</span>
          <input
            ref={inputRef}
            name="q"
            value={query.q}
            onChange={(e) => {
              onText(e.target.value);
              track(e.target);
              setOpen(true);
              setPick(-1);
            }}
            onSelect={(e) => track(e.currentTarget)}
            onFocus={(e) => track(e.currentTarget)}
            onBlur={() => {
              setCaret(null);
              setOpen(false);
            }}
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={!!shown}
            aria-controls={shown ? 'tag-suggestions' : undefined}
            aria-activedescendant={shown && pick >= 0 ? `tag-suggestion-${pick}` : undefined}
            onKeyDown={(e) => {
              if (shown && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
                e.preventDefault();
                const n = shown.items.length;
                setPick((p) => (e.key === 'ArrowDown' ? (p + 1) % n : (p - 1 + n) % n));
                return;
              }
              if (shown && pick >= 0 && (e.key === 'Enter' || e.key === 'Tab')) {
                e.preventDefault();
                take(pick);
                return;
              }
              if (shown && e.key === 'Escape') {
                e.preventDefault();
                setOpen(false);
                return;
              }
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
      {shown && (
        <ul id="tag-suggestions" className={s.suggest} role="listbox" aria-label="Tag suggestions">
          {shown.items.map((it, i) => (
            <li
              key={it.text}
              id={`tag-suggestion-${i}`}
              role="option"
              aria-selected={i === pick}
              // mousedown, not click: a click would blur the box first and close the list
              onMouseDown={(e) => {
                e.preventDefault();
                take(i);
              }}
            >
              <span className="mono">{it.text}</span>
              {!it.complete && <span className={s.hint}>key: pick to see its values</span>}
            </li>
          ))}
        </ul>
      )}
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
