// The mark: an S drawn from five strata, the middle layer (the spine) lit
// orange, the colour of data that was read. Layers of different lengths keep
// it from reading as a menu icon at small sizes.
const LAYERS: [x: number, w: number][] = [
  [5, 12],
  [3, 5.5],
  [3, 14],
  [11.5, 5.5],
  [3, 12],
];

export function LogoMark({ size = 20 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 20 20" aria-hidden="true" focusable="false">
      {LAYERS.map(([x, w], i) => (
        <rect
          key={i}
          x={x}
          y={0.5 + i * 4.05}
          width={w}
          height={2.8}
          rx={1}
          fill={i === 2 ? 'var(--read)' : 'currentColor'}
          opacity={i === 2 ? 1 : 0.5}
        />
      ))}
    </svg>
  );
}
