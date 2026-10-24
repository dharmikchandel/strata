import { useEffect, useRef, useState } from 'react';
import type { Corpus } from '../lib/api';
import { PRESETS, displayTime, rangeLabel, validateRange, type Problem, type Range } from '../lib/query';
import { CaretDownIcon, ClockIcon } from './icons';
import s from './TimeRange.module.css';

interface Props {
  range: Range;
  corpus: Corpus | null;
  onPick: (r: Range) => void; // a preset or an applied custom range; runs the search
}

const plain = { autoComplete: 'off', autoCapitalize: 'off', autoCorrect: 'off', spellCheck: false } as const;

// The time control: a button showing the current range, opening a panel of
// presets and a custom From/To. The panel uses the browser's popover API, so
// it sits above everything (the search bar clips its own overflow on phones),
// and Esc or a click outside closes it without any code here.
export function TimeRange({ range, corpus, onPick }: Props) {
  const button = useRef<HTMLButtonElement>(null);
  const pop = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [problem, setProblem] = useState<Problem | null>(null);

  // Opening: start the custom fields from the range in use, and place the
  // panel under the button. The position is set as custom properties through
  // the CSSOM, which the Content-Security-Policy allows (style attributes in
  // markup it would not).
  useEffect(() => {
    const el = pop.current;
    if (!el) return;
    const place = () => {
      const r = button.current?.getBoundingClientRect();
      if (!r) return;
      el.style.setProperty('--pop-top', `${Math.round(r.bottom + 6)}px`);
      el.style.setProperty('--pop-right', `${Math.max(16, Math.round(window.innerWidth - r.right))}px`);
    };
    const before = (e: Event) => {
      if ((e as ToggleEvent).newState !== 'open') return;
      place();
      setFrom(range.kind === 'between' ? range.from : '');
      setTo(range.kind === 'between' ? range.to : '');
      setProblem(null);
    };
    const toggled = (e: Event) => setOpen((e as ToggleEvent).newState === 'open');
    el.addEventListener('beforetoggle', before);
    el.addEventListener('toggle', toggled);
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, { passive: true });
    return () => {
      el.removeEventListener('beforetoggle', before);
      el.removeEventListener('toggle', toggled);
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place);
    };
  }, [range]);

  const pick = (r: Range) => {
    pop.current?.hidePopover();
    onPick(r);
  };
  const apply = () => {
    const p = validateRange(from, to, corpus);
    setProblem(p);
    if (!p) pick(from.trim() || to.trim() ? { kind: 'between', from: from.trim(), to: to.trim() } : { kind: 'all' });
  };
  // Enter in a field applies the range here instead of submitting the search
  // form the panel sits in.
  const enter = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      e.preventDefault();
      apply();
    }
  };
  const bad = (f: 'from' | 'to') => (problem?.field === f ? { 'aria-invalid': true, 'aria-describedby': 'range-error' } : {});

  return (
    <>
      <button
        ref={button}
        type="button"
        className={s.trigger}
        popoverTarget="time-range"
        aria-controls="time-range"
        aria-expanded={open}
        aria-haspopup="dialog"
      >
        <ClockIcon />
        <span className={s.label}>{rangeLabel(range)}</span>
        <CaretDownIcon className={s.caret} />
      </button>

      <div ref={pop} id="time-range" popover="auto" className={s.pop} role="dialog" aria-label="Time range">
        <p className={s.note}>
          {corpus ? (
            <>
              Counted back from the newest stored line, <span className="mono">{corpus.last}</span> UTC.
            </>
          ) : (
            'Counted back from the newest stored line.'
          )}
        </p>
        <div className={s.presets} role="group" aria-label="Presets">
          {PRESETS.map((p) => (
            <button
              key={p.label}
              type="button"
              aria-pressed={range.kind === 'last' && range.seconds === p.seconds}
              onClick={() => pick({ kind: 'last', seconds: p.seconds })}
            >
              {p.label}
            </button>
          ))}
          <button type="button" aria-pressed={range.kind === 'all'} onClick={() => pick({ kind: 'all' })}>
            All
          </button>
        </div>

        <fieldset className={s.custom}>
          <legend>Custom range (UTC)</legend>
          <label>
            <span>From</span>
            <input value={from} onChange={(e) => setFrom(e.target.value)} onKeyDown={enter} placeholder={corpus?.first ?? 'YYYY-MM-DD HH:MM'} inputMode="numeric" {...plain} {...bad('from')} />
          </label>
          <label>
            <span>To</span>
            <input value={to} onChange={(e) => setTo(e.target.value)} onKeyDown={enter} placeholder={corpus?.last ?? 'YYYY-MM-DD HH:MM'} inputMode="numeric" {...plain} {...bad('to')} />
          </label>
        </fieldset>
        {problem && (
          <p id="range-error" className={s.error} role="alert">
            {problem.message}
          </p>
        )}
        <div className={s.actions}>
          {corpus && (
            <button
              type="button"
              className={s.quiet}
              onClick={() => {
                setFrom(displayTime(corpus.min_input));
                setTo(displayTime(corpus.max_input));
                setProblem(null);
              }}
            >
              Fill in the stored range
            </button>
          )}
          <button type="button" className={s.apply} onClick={apply}>
            Apply
          </button>
        </div>
      </div>
    </>
  );
}
