/**
 * One side of the curly braces that frame the sign-in and setup sheets. Both sides
 * are the same path, the right one mirrored, so they match to the pixel: a font's
 * { and } have different side bearings, which left the gaps to the sheet unequal.
 */
export function Brace({ side }: { side: 'left' | 'right' }) {
  return <svg aria-hidden="true" className={`desktop-brace desktop-brace-${side}`} viewBox="0 0 100 400" preserveAspectRatio="xMidYMid meet">
    <path d="M88 12 C56 12 46 28 46 62 L46 158 C46 184 32 198 12 200 C32 202 46 216 46 242 L46 338 C46 372 56 388 88 388" />
  </svg>;
}
