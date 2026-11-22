export const fontFamilies = [
  'Bricolage Grotesque', 'Instrument Sans', 'Space Grotesk', 'Parcel Sans',
  'Parcel Serif', 'Parcel Mono', 'Fraunces', 'Geist', 'Geist Mono',
  'Roboto Slab', 'Manrope', 'DM Serif Display', 'Newsreader', 'JetBrains Mono',
  'Syne', 'Unbounded', 'Literata', 'Archivo', 'Atkinson Hyperlegible', 'Silkscreen',
] as const;

export type FontFamily = typeof fontFamilies[number];
export const monoFamilies: readonly FontFamily[] = ['Parcel Mono', 'Geist Mono', 'JetBrains Mono'];
export const fontStack = (family: FontFamily) => `"${family}", ${monoFamilies.includes(family) ? 'monospace' : 'sans-serif'}`;

const embeddedFaces = new Map<FontFamily, Promise<string>>();
export function embeddedFontCSS(family: FontFamily): Promise<string> {
  const existing = embeddedFaces.get(family);
  if (existing) return existing;
  const task = (async () => {
    const upstream = family.replace('Parcel', 'IBM Plex');
    const slug = upstream.toLowerCase().replace(/ /g, '');
    const staticFamily = ['Parcel Serif', 'Parcel Mono', 'Atkinson Hyperlegible', 'Silkscreen', 'DM Serif Display'].includes(family);
    const weights = staticFamily ? family === 'DM Serif Display' ? ['400'] : ['400', '700'] : ['variable'];
    const rules: string[] = [];
    for (const weight of weights) {
      const response = await fetch(`/fonts/${slug}-${weight}-latin.woff2`);
      if (!response.ok) throw new Error('Bundled reading font unavailable');
      const bytes = new Uint8Array(await response.arrayBuffer());
      let binary = '';
      for (const byte of bytes) binary += String.fromCharCode(byte);
      rules.push(`@font-face { font-family: "${family}"; font-weight: ${weight === 'variable' ? '100 900' : weight}; src: url(data:font/woff2;base64,${btoa(binary)}) format('woff2'); }`);
    }
    return rules.join('\n');
  })();
  embeddedFaces.set(family, task);
  task.catch(() => embeddedFaces.delete(family));
  return task;
}
