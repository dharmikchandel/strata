import type { Example } from '../lib/api';
import s from './Examples.module.css';

interface Props {
  examples: Example[];
  active: number | null;
  onRun: (i: number) => void;
}

// One-click searches that each show a different way segments get skipped.
// The note of the one on screen says what to look for.
export function Examples({ examples, active, onRun }: Props) {
  if (examples.length === 0) return null;
  const note = active !== null ? examples[active]?.note : null;
  return (
    <section className={s.examples} aria-labelledby="examples-title">
      <div className={s.row}>
        <h2 id="examples-title" className={s.title}>Try</h2>
        <ul className={s.list}>
          {examples.map((ex, i) => (
            <li key={i}>
              <button
                type="button"
                className={s.pill}
                aria-current={active === i ? 'true' : undefined}
                aria-describedby={active === i && note ? 'example-note' : undefined}
                onClick={() => onRun(i)}
              >
                {ex.title}
              </button>
            </li>
          ))}
        </ul>
      </div>
      {note && (
        <p id="example-note" className={s.note}>
          {note}
        </p>
      )}
    </section>
  );
}
