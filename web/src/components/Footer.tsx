import { ArrowUpRightIcon } from './icons';
import s from './Footer.module.css';

// wide: span the window with the app's gutters, so its rule lines up with
// the full-width panels above it.
export function Footer({ credit, aboutUrl, wide }: { credit?: string; aboutUrl?: string; wide?: boolean }) {
  if (!credit && !aboutUrl) return null;
  return (
    <footer className={s.foot} data-wide={wide || undefined}>
      {credit && <p className={s.credit}>{credit}</p>}
      {aboutUrl && (
        <a className={s.about} href={aboutUrl} rel="noopener">
          How this works <ArrowUpRightIcon />
        </a>
      )}
    </footer>
  );
}
