import { LogoMark } from './Logo';
import { ThemeToggle } from './ThemeToggle';
import { ArrowUpRightIcon } from './icons';
import s from './TopBar.module.css';

interface Props {
  page: 'landing' | 'search' | 'system' | 'benchmarks' | 'design';
  sourceUrl?: string; // the project's home (the "about" link), when configured
  wide?: boolean; // span the window, like the app below it
}

// The bar shared by both pages: the mark (home), the sections of the story,
// and on the landing page the way into the demo.
export function TopBar({ page, sourceUrl, wide }: Props) {
  return (
    <header className={s.bar}>
      <div className={s.inner} data-wide={wide || undefined}>
        <a className={s.brand} href="/" aria-label="Strata home">
          <LogoMark size={22} />
          <span className={s.word}>Strata</span>
        </a>
        <nav className={s.nav} aria-label="Main">
          <a href="/search/" aria-current={page === 'search' ? 'page' : undefined}>Search</a>
          <a href="/system/" aria-current={page === 'system' ? 'page' : undefined}>System</a>
          <a href="/benchmarks/" aria-current={page === 'benchmarks' ? 'page' : undefined}>Benchmarks</a>
          <a href="/design/" aria-current={page === 'design' ? 'page' : undefined}>Design</a>
          {sourceUrl && (
            <a href={sourceUrl} rel="noopener">
              Source <ArrowUpRightIcon />
            </a>
          )}
        </nav>
        <div className={s.end}>
          <ThemeToggle />
          {page === 'landing' && (
            <a className={s.cta} href="/search/">
              Open the demo
            </a>
          )}
        </div>
      </div>
    </header>
  );
}
