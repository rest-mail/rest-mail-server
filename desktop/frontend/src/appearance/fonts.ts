export const fontFamilies = [
  'Bricolage Grotesque', 'Instrument Sans', 'Space Grotesk', 'IBM Plex Sans',
  'IBM Plex Serif', 'IBM Plex Mono', 'Fraunces', 'Geist', 'Geist Mono',
  'Roboto Slab', 'Manrope', 'DM Serif Display', 'Newsreader', 'JetBrains Mono',
  'Syne', 'Unbounded', 'Literata', 'Archivo', 'Atkinson Hyperlegible', 'Silkscreen',
] as const;

export type FontFamily = typeof fontFamilies[number];
export const monoFamilies: readonly FontFamily[] = ['IBM Plex Mono', 'Geist Mono', 'JetBrains Mono'];
export const fontStack = (family: FontFamily) => `"${family}", ${monoFamilies.includes(family) ? 'monospace' : 'sans-serif'}`;
