import { forwardRef } from 'react';
import type { Corpus } from '../lib/api';
import { filterCount, type Field, type FormState } from '../lib/query';
import { SearchIcon, SlidersIcon } from './icons';
import s from './SearchForm.module.css';

interface Props {
  form: FormState;
  corpus: Corpus | null;
  busy: boolean;
  invalid: Field | null; // the field the last check found a problem in
  filtersOpen: boolean;
  onFiltersOpen: (open: boolean) => void;
  onChange: (f: FormState) => void;
  onSubmit: () => void;
}

// Inputs for log terms: no capitalising, correcting or spell-checking.
const plain = { autoComplete: 'off', autoCapitalize: 'off', autoCorrect: 'off', spellCheck: false } as const;

export const SearchForm = forwardRef<HTMLInputElement, Props>(function SearchForm(
  { form, corpus, busy, invalid, filtersOpen, onFiltersOpen, onChange, onSubmit },
  wordsRef,
) {
  const set = (k: keyof FormState) => (e: React.ChangeEvent<HTMLInputElement>) => onChange({ ...form, [k]: e.target.value });
  const active = filterCount(form);
  // Problems are found by the page's own check (lib/query.ts validate) and
  // shown under the bar, so the browser's validation bubble is switched off.
  const bad = (k: Field) => (invalid === k ? { 'aria-invalid': true, 'aria-describedby': 'search-error' } : {});

  return (
    <form
      className={s.form}
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <div className={s.bar}>
        <label className={s.words}>
          <SearchIcon className={s.searchIcon} />
          <span className="sr-only">Words</span>
          <input
            ref={wordsRef}
            name="text"
            value={form.text}
            onChange={set('text')}
            placeholder="Search words, e.g. parity error"
            enterKeyHint="search"
            {...plain}
          />
        </label>

        <button
          type="button"
          className={s.filtersToggle}
          aria-expanded={filtersOpen}
          aria-controls="filters"
          onClick={() => onFiltersOpen(!filtersOpen)}
        >
          <SlidersIcon />
          Filters{active ? <span className={s.count}>{active}</span> : null}
        </button>

        <div id="filters" className={s.filters} data-open={filtersOpen || undefined}>
          <label className={s.field}>
            <span className={s.caption}>Tags</span>
            <input name="tag" value={form.tag} onChange={set('tag')} placeholder="e.g. level=FATAL" {...plain} />
          </label>
          <label className={s.field}>
            <span className={s.caption}>From (UTC)</span>
            <input name="from" value={form.from} onChange={set('from')} placeholder={corpus ? corpus.first : 'YYYY-MM-DD HH:MM'} inputMode="numeric" {...plain} {...bad('from')} />
          </label>
          <label className={s.field}>
            <span className={s.caption}>To (UTC)</span>
            <input name="to" value={form.to} onChange={set('to')} placeholder={corpus ? corpus.last : 'YYYY-MM-DD HH:MM'} inputMode="numeric" {...plain} {...bad('to')} />
          </label>
          <label className={`${s.field} ${s.limit}`}>
            <span className={s.caption}>Max lines</span>
            <input name="limit" value={form.limit} onChange={set('limit')} inputMode="numeric" {...plain} {...bad('limit')} />
          </label>
        </div>

        <button type="submit" className={s.submit} disabled={busy}>
          {busy ? 'Searching…' : 'Search'}
        </button>
      </div>
      <p className={s.hint}>A line matches when it contains every word, in any order. Separate several tags with spaces.</p>
    </form>
  );
});
