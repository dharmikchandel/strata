// One small icon set, drawn on a 16px grid with a 1.5 stroke.
type IconProps = { className?: string };

const base = {
  width: 16,
  height: 16,
  viewBox: '0 0 16 16',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.5,
  strokeLinecap: 'round' as const,
  strokeLinejoin: 'round' as const,
  'aria-hidden': true,
  focusable: false,
};

export const SearchIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <circle cx="7" cy="7" r="4.25" />
    <path d="M10.25 10.25 13.5 13.5" />
  </svg>
);

export const SlidersIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="M2.5 4.5h6M11.5 4.5h2M2.5 11.5h2M7.5 11.5h6" />
    <circle cx="10" cy="4.5" r="1.5" />
    <circle cx="6" cy="11.5" r="1.5" />
  </svg>
);

export const ChevronIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="m6 4 4 4-4 4" />
  </svg>
);

export const SunIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <circle cx="8" cy="8" r="2.75" />
    <path d="M8 1.75v1.5M8 12.75v1.5M1.75 8h1.5M12.75 8h1.5M3.6 3.6l1.05 1.05M11.35 11.35l1.05 1.05M3.6 12.4l1.05-1.05M11.35 4.65l1.05-1.05" />
  </svg>
);

export const MoonIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="M13.25 9.6A5.5 5.5 0 0 1 6.4 2.75a5.5 5.5 0 1 0 6.85 6.85Z" />
  </svg>
);

export const AlertIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <circle cx="8" cy="8" r="6" />
    <path d="M8 4.75v3.75M8 11.1v.15" />
  </svg>
);

export const ArrowRightIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="M3 8h10M9 4l4 4-4 4" />
  </svg>
);

export const ArrowUpRightIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="M5 11 11 5M6 5h5v5" />
  </svg>
);

export const ClockIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <circle cx="8" cy="8" r="5.75" />
    <path d="M8 4.75V8l2.25 1.5" />
  </svg>
);

export const CaretDownIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="m4.5 6.5 3.5 3.5 3.5-3.5" />
  </svg>
);

export const CopyIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <rect x="5.25" y="5.25" width="8" height="8" rx="1.5" />
    <path d="M10.75 5.25V3.75a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h1.5" />
  </svg>
);

export const PlusIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="M8 3.5v9M3.5 8h9" />
  </svg>
);

export const CheckIcon = (p: IconProps) => (
  <svg {...base} className={p.className}>
    <path d="m3.5 8.5 3 3 6-7" />
  </svg>
);
