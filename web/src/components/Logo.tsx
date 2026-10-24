// The mark is the search readout in miniature: a row of segments, all skipped
// but one, which was read. The whole idea of the product in one glyph.
export function LogoMark({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true" focusable="false">
      <rect x="1" y="14" width="3" height="4" rx="1" fill="currentColor" opacity="0.4" />
      <rect x="4.75" y="14" width="3" height="4" rx="1" fill="currentColor" opacity="0.4" />
      <rect x="8.5" y="2" width="3" height="16" rx="1" fill="var(--read)" />
      <rect x="12.25" y="14" width="3" height="4" rx="1" fill="currentColor" opacity="0.4" />
      <rect x="16" y="14" width="3" height="4" rx="1" fill="currentColor" opacity="0.4" />
    </svg>
  );
}
