import { useEffect, useState } from 'react';
import { MoonIcon, SunIcon } from './icons';
import s from './ThemeToggle.module.css';

type Theme = 'light' | 'dark';
const KEY = 'strata-theme';

function systemTheme(): Theme {
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
}

function storedTheme(): Theme | null {
  try {
    const v = localStorage.getItem(KEY);
    return v === 'light' || v === 'dark' ? v : null;
  } catch {
    return null; // storage can be blocked; the system setting still applies
  }
}

// Follows the system setting until the visitor picks one; the pick is
// remembered in this browser only.
export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(() => storedTheme() ?? systemTheme());

  useEffect(() => {
    const picked = storedTheme();
    if (picked) document.documentElement.dataset.theme = picked;
  }, []);

  function flip() {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    setTheme(next);
    document.documentElement.dataset.theme = next;
    try {
      localStorage.setItem(KEY, next);
    } catch {
      /* not remembered, still applied */
    }
  }

  const label = theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme';
  return (
    <button type="button" className={s.toggle} onClick={flip} aria-label={label} title={label}>
      {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
    </button>
  );
}
