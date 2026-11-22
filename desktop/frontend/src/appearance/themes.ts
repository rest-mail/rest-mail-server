import type { FontFamily } from './fonts';

export interface Palette {
  sidebar: string; sidebarInk: string; sidebarMuted: string;
  sidebarSelection: string; sidebarSelectionInk: string;
  list: string; reader: string; sheet: string; ink: string; muted: string;
  accent: string; accentInk: string; selection: string; selectionInk: string;
  border: string; unread: string; badge: string; badgeInk: string;
  danger: string; success: string; brace: string;
}
export interface Typefaces { display: FontFamily; ui: FontFamily; reading: FontFamily; mono: FontFamily }
export interface Shape { radius: number; density: number; scale: number }
export interface Theme {
  id: string; name: string; character: string; dark: boolean;
  colours: Palette; fonts: Typefaces; shape: Shape; texture: string;
}

export function luminance(hex: string) {
  const channels = hex.slice(1).match(/../g)!.map(channel => parseInt(channel, 16) / 255).map(channel => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4);
  return channels[0] * .2126 + channels[1] * .7152 + channels[2] * .0722;
}
export function contrast(first: string, second: string) {
  const values = [luminance(first), luminance(second)].sort((left, right) => right - left);
  return (values[0] + .05) / (values[1] + .05);
}
export function contrastInk(background: string) { return contrast('#ffffff', background) >= contrast('#111111', background) ? '#ffffff' : '#111111'; }

export const defaultTheme: Theme = {
  id: 'rest-mail', name: 'REST Mail', character: 'Confident navy. A little sunshine. Unmistakably yours.', dark: false,
  colours: {
    sidebar: '#1a2a4a', sidebarInk: '#ffffff', sidebarMuted: '#c2cde0',
    sidebarSelection: '#ffcc00', sidebarSelectionInk: '#1a2a4a',
    list: '#f3f5fa', reader: '#ffffff', sheet: '#ffffff', ink: '#1a2a4a', muted: '#53627a',
    accent: '#ffcc00', accentInk: '#1a2a4a', selection: '#2c4373', selectionInk: '#ffffff',
    border: '#7b879b', unread: '#2c4373', badge: '#ffcc00', badgeInk: '#1a2a4a',
    danger: '#b42318', success: '#16713f', brace: '#6e87b6',
  },
  fonts: { display: 'Bricolage Grotesque', ui: 'Instrument Sans', reading: 'Instrument Sans', mono: 'Geist Mono' },
  shape: { radius: 10, density: 36, scale: 1 }, texture: 'none',
};

function palette(input: Pick<Palette, 'sidebar' | 'sidebarInk' | 'sidebarMuted' | 'list' | 'reader' | 'ink' | 'muted' | 'accent' | 'selection' | 'border' | 'danger' | 'success' | 'brace'>): Palette {
  const sidebarSelection = contrast(input.accent, input.sidebar) >= 3 ? input.accent : input.selection;
  return { ...input, sidebarSelection, sidebarSelectionInk: contrastInk(sidebarSelection), sheet: input.reader, accentInk: contrastInk(input.accent), selectionInk: contrastInk(input.selection), unread: input.selection, badge: input.accent, badgeInk: contrastInk(input.accent) };
}

export const builtInThemes: readonly Theme[] = [defaultTheme, {
  id: 'letterpress', name: 'Letterpress', character: 'Slow mornings, warm paper, beautifully long letters.', dark: false,
  colours: palette({ sidebar: '#3a2d27', sidebarInk: '#fff4df', sidebarMuted: '#e2ccae', list: '#f4eedf', reader: '#fffaf0', ink: '#30271f', muted: '#6b5844', accent: '#e9b65f', selection: '#70422f', border: '#887661', danger: '#a52d25', success: '#22633b', brace: '#d3aa73' }),
  fonts: { display: 'DM Serif Display', ui: 'Instrument Sans', reading: 'Newsreader', mono: 'Parcel Mono' }, shape: { radius: 5, density: 38, scale: 1.04 }, texture: 'radial-gradient(ellipse at top left, rgb(233 182 95 / 8%), transparent 70%)',
}, {
  id: 'phosphor', name: 'Phosphor', character: 'A love letter to the terminal. All signal, no noise.', dark: true,
  colours: palette({ sidebar: '#08140f', sidebarInk: '#cef7d4', sidebarMuted: '#93c49d', list: '#0e1d17', reader: '#10231b', ink: '#cef7d4', muted: '#93c49d', accent: '#b8fa74', selection: '#b8fa74', border: '#6e9e7b', danger: '#ffaaaa', success: '#b8fa74', brace: '#6e9e7b' }),
  fonts: { display: 'Space Grotesk', ui: 'Parcel Mono', reading: 'Parcel Sans', mono: 'JetBrains Mono' }, shape: { radius: 3, density: 32, scale: 1 }, texture: 'repeating-linear-gradient(0deg, transparent, transparent 3px, rgb(184 250 116 / 3%) 3px, rgb(184 250 116 / 3%) 4px)',
}, {
  id: 'clarity', name: 'Clarity', character: 'Big-hearted legibility. Absolutely nothing in the way.', dark: false,
  colours: palette({ sidebar: '#000000', sidebarInk: '#ffffff', sidebarMuted: '#dddddd', list: '#ffffff', reader: '#ffffff', ink: '#000000', muted: '#333333', accent: '#ffe500', selection: '#000000', border: '#555555', danger: '#a40000', success: '#00602b', brace: '#ffffff' }),
  fonts: { display: 'Atkinson Hyperlegible', ui: 'Atkinson Hyperlegible', reading: 'Literata', mono: 'Parcel Mono' }, shape: { radius: 6, density: 42, scale: 1.08 }, texture: 'none',
}, {
  id: 'sorbet', name: 'Sorbet', character: 'Lilac clouds, rose accents, a wonderfully soft landing.', dark: false,
  colours: palette({ sidebar: '#efe2f5', sidebarInk: '#32243e', sidebarMuted: '#675171', list: '#f9f1fa', reader: '#fffbff', ink: '#3e2849', muted: '#70547a', accent: '#efb9c9', selection: '#68457c', border: '#917497', danger: '#a32b52', success: '#276d4b', brace: '#8a5e9c' }),
  fonts: { display: 'Fraunces', ui: 'Manrope', reading: 'Geist', mono: 'Geist Mono' }, shape: { radius: 18, density: 40, scale: 1 }, texture: 'radial-gradient(ellipse at top, rgb(255 255 255 / 22%), transparent 70%)',
}, {
  id: 'midnight', name: 'Midnight', character: 'Inky blue, electric lavender. For your finest late ideas.', dark: true,
  colours: palette({ sidebar: '#0c1125', sidebarInk: '#ecedff', sidebarMuted: '#b8bfdd', list: '#12192f', reader: '#182039', ink: '#ecedff', muted: '#b8bfdd', accent: '#b7b2ff', selection: '#b7b2ff', border: '#7d88ae', danger: '#ffb2bf', success: '#93e5bf', brace: '#7d88ae' }),
  fonts: { display: 'Space Grotesk', ui: 'Geist', reading: 'Newsreader', mono: 'Geist Mono' }, shape: { radius: 12, density: 36, scale: 1 }, texture: 'radial-gradient(ellipse at top right, rgb(183 178 255 / 8%), transparent 70%)',
}, {
  id: 'concrete', name: 'Concrete', character: 'Loud type, sharp edges. The inbox as a manifesto.', dark: false,
  colours: palette({ sidebar: '#f5f330', sidebarInk: '#101010', sidebarMuted: '#414100', list: '#f1f1eb', reader: '#ffffff', ink: '#101010', muted: '#50504a', accent: '#f5f330', selection: '#111111', border: '#70706a', danger: '#b02523', success: '#126237', brace: '#101010' }),
  fonts: { display: 'Unbounded', ui: 'Archivo', reading: 'Roboto Slab', mono: 'Parcel Mono' }, shape: { radius: 0, density: 34, scale: 1.02 }, texture: 'none',
}, {
  id: 'system-seven', name: 'System Seven', character: 'Hello again. A pixel of nostalgia, minus the floppy disks.', dark: false,
  colours: palette({ sidebar: '#deded8', sidebarInk: '#171717', sidebarMuted: '#464642', list: '#f5f5ef', reader: '#fffef8', ink: '#171717', muted: '#57574d', accent: '#b5d6f6', selection: '#1b3264', border: '#73736d', danger: '#a52c25', success: '#206335', brace: '#484842' }),
  fonts: { display: 'Silkscreen', ui: 'Geist', reading: 'Parcel Sans', mono: 'Parcel Mono' }, shape: { radius: 1, density: 30, scale: 1 }, texture: 'repeating-linear-gradient(45deg, transparent, transparent 2px, rgb(0 0 0 / 2%) 2px, rgb(0 0 0 / 2%) 3px)',
}, {
  id: 'canopy', name: 'Canopy', character: 'Deep forest greens. Room to breathe between messages.', dark: true,
  colours: palette({ sidebar: '#0d201c', sidebarInk: '#eef6d9', sidebarMuted: '#bbcfad', list: '#172c24', reader: '#1c3329', ink: '#eef6d9', muted: '#bbcfad', accent: '#d4e78d', selection: '#d4e78d', border: '#7c9d87', danger: '#ffb9a4', success: '#c0e9a5', brace: '#7c9d87' }),
  fonts: { display: 'Fraunces', ui: 'Manrope', reading: 'Literata', mono: 'JetBrains Mono' }, shape: { radius: 14, density: 38, scale: 1.02 }, texture: 'radial-gradient(ellipse at bottom left, rgb(212 231 141 / 6%), transparent 75%)',
}, {
  id: 'afterglow', name: 'Afterglow', character: 'Apricot light, plum shadows. One more golden hour.', dark: false,
  colours: palette({ sidebar: '#341c38', sidebarInk: '#fff1ed', sidebarMuted: '#f4d5d2', list: '#fff1ed', reader: '#fff8f3', ink: '#462c39', muted: '#795462', accent: '#ffc187', selection: '#843a57', border: '#a27985', danger: '#ae2740', success: '#32663f', brace: '#e5a188' }),
  fonts: { display: 'Syne', ui: 'Space Grotesk', reading: 'Parcel Serif', mono: 'Geist Mono' }, shape: { radius: 16, density: 38, scale: 1 }, texture: 'linear-gradient(155deg, #341c38 5%, #5c293b 100%)',
}];

export function themeByName(value: string) {
  const theme = builtInThemes.find(theme => theme.id === value || theme.name.toLowerCase() === value.toLowerCase());
  if (!theme) throw new Error(`Unknown theme: ${value}`);
  return theme;
}
