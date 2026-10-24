import type { Corpus } from '../lib/api';
import { formatBytes, n, plural } from '../lib/format';
import { LogoMark } from './Logo';
import { ThemeToggle } from './ThemeToggle';
import s from './TopBar.module.css';

export function TopBar({ corpus, loaded }: { corpus: Corpus | null; loaded: boolean }) {
  return (
    <header className={s.bar}>
      <div className={s.inner}>
        <a className={s.brand} href="/" aria-label="Strata, search home">
          <LogoMark size={20} />
          <span className={s.word}>Strata</span>
        </a>
        <div className={s.facts}>
          {loaded && corpus && (
            <p className={s.corpus}>
              {corpus.name && <span className={s.name}>{corpus.name}</span>}
              <span className="mono">{n(corpus.entries)} lines</span>
              <span className="mono">{plural(corpus.segments, 'segment')}</span>
              <span className="mono">{formatBytes(corpus.stored_bytes)}</span>
              <span className={`mono ${s.range}`}>
                {corpus.first} → {corpus.last} UTC
              </span>
            </p>
          )}
        </div>
        <ThemeToggle />
      </div>
    </header>
  );
}
