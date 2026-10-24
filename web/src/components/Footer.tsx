import { ArrowUpRightIcon } from './icons';
import s from './Footer.module.css';

export function Footer({ credit, aboutUrl }: { credit?: string; aboutUrl?: string }) {
  if (!credit && !aboutUrl) return null;
  return (
    <footer className={s.foot}>
      {credit && <p className={s.credit}>{credit}</p>}
      {aboutUrl && (
        <a className={s.about} href={aboutUrl} rel="noopener">
          How this works <ArrowUpRightIcon />
        </a>
      )}
    </footer>
  );
}
