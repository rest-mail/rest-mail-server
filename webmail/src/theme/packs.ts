/**
 * Theme packs: one registry describing every skin the webmail can wear.
 *
 * A pack is two things:
 *  1. tokens   — the CSS class of the same name in index.css (colours, radii).
 *                The registry only carries the metadata the UI needs to show
 *                swatches and to paint the sandboxed message iframe.
 *  2. variants — the structural axes. Tokens alone cannot say "rows become
 *                cards" or "actions become words", so each pack pins one value
 *                per axis and components branch on them. Adding a theme that
 *                reuses existing axis values touches only this file + CSS.
 */

export type ThemeId =
  | 'dawn' | 'linen' | 'slate' | 'dusk' | 'midnight' | 'forest' | 'neon' | 'aurora' | 'industrial'
  | 'brand' | 'ledger' | 'nightops';

/** How message rows are separated / surfaced. */
export type Surface = 'dividers' | 'rules' | 'dense' | 'cards';
/** How an unread message announces itself. */
export type UnreadSignal = 'weight' | 'rail' | 'dot';
/** Sender identity chip in the list; also the reader avatar shape. */
export type Identity = 'none' | 'squircle' | 'circle';
/** Family used for the list / the reader chrome and plain-text bodies. */
export type Typeface = 'mono' | 'sans' | 'serif';
/** How the reader action bar labels itself. */
export type ActionStyle = 'icons' | 'words' | 'keyed';

export interface ThemeVariants {
  surface: Surface;
  unread: UnreadSignal;
  identity: Identity;
  listType: Typeface;
  readerType: Typeface;
  actions: ActionStyle;
}

/** Palette for the sandboxed message iframe, which cannot inherit CSS vars. */
export interface FramePalette {
  scheme: 'light' | 'dark';
  bg: string;
  fg: string;
  link: string;
  border: string;
  muted: string;
  font: string;
}

export interface ThemePack {
  id: ThemeId;
  name: string;
  group: 'Restmail' | 'Light' | 'Mid' | 'Dark' | 'Vivid' | 'Tech';
  tagline: string;
  dark: boolean;
  swatch: { bg: string; accent: string };
  variants: ThemeVariants;
  frame: FramePalette;
}

export const TYPEFACE_CLASS: Record<Typeface, string> = {
  mono: 'typeface-mono',
  sans: 'typeface-sans',
  serif: 'typeface-serif',
};

const MONO = "'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace";
const SANS = "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif";
const SERIF = "'Iowan Old Style', 'Palatino Linotype', Palatino, Georgia, 'Times New Roman', serif";

/** The look every shipped theme had before variants existed — keep it frozen. */
const CLASSIC: ThemeVariants = {
  surface: 'dividers',
  unread: 'weight',
  identity: 'none',
  listType: 'mono',
  readerType: 'mono',
  actions: 'icons',
};

const frameLight = (over: Partial<FramePalette> = {}): FramePalette => ({
  scheme: 'light',
  bg: '#ffffff',
  fg: '#1a1a1a',
  link: '#2563eb',
  border: '#e5e7eb',
  muted: '#666666',
  font: MONO,
  ...over,
});

const frameDark = (over: Partial<FramePalette> = {}): FramePalette => ({
  scheme: 'dark',
  bg: '#181a1f',
  fg: '#e6e6e6',
  link: '#7aa2f7',
  border: '#2c3038',
  muted: '#9aa0a6',
  font: MONO,
  ...over,
});

export const THEME_PACKS: ThemePack[] = [
  // ── Restmail originals ──────────────────────────────────────────
  {
    id: 'brand',
    name: 'Brand',
    group: 'Restmail',
    tagline: 'Navy ink, one loud yellow note. The official-client look.',
    dark: false,
    swatch: { bg: '#ffffff', accent: '#FFCC00' },
    variants: { ...CLASSIC, unread: 'rail', identity: 'squircle', listType: 'sans', readerType: 'sans' },
    frame: frameLight({ fg: '#22304a', link: '#2c4373', border: '#e4e9f1', muted: '#61708b', font: SANS }),
  },
  {
    id: 'ledger',
    name: 'Ledger',
    group: 'Restmail',
    tagline: 'Editorial paper: serif subjects, full-bleed rules, actions as words.',
    dark: false,
    swatch: { bg: '#faf7f0', accent: '#96351f' },
    variants: { surface: 'rules', unread: 'weight', identity: 'none', listType: 'serif', readerType: 'serif', actions: 'words' },
    frame: frameLight({ bg: '#faf7f0', fg: '#26271f', link: '#96351f', border: '#e4ddcd', muted: '#6f6a5c', font: SERIF }),
  },
  {
    id: 'nightops',
    name: 'Night Ops',
    group: 'Restmail',
    tagline: 'Terminal for the self-hosted: dense rows, key hints, amber on black.',
    dark: true,
    swatch: { bg: '#0b0d12', accent: '#FFB020' },
    variants: { surface: 'dense', unread: 'rail', identity: 'none', listType: 'mono', readerType: 'mono', actions: 'keyed' },
    frame: frameDark({ bg: '#0b0d12', fg: '#b9c3d0', link: '#ffb020', border: '#1c232d', muted: '#7a8494', font: MONO }),
  },

  // ── Existing palettes, classic variants ─────────────────────────
  {
    id: 'dawn', name: 'Dawn', group: 'Light', tagline: 'Light, blue accent.',
    dark: false, swatch: { bg: '#ffffff', accent: '#2563eb' }, variants: CLASSIC, frame: frameLight(),
  },
  {
    id: 'linen', name: 'Linen', group: 'Light', tagline: 'Warm light, amber accent.',
    dark: false, swatch: { bg: '#fdf6e3', accent: '#c27835' }, variants: CLASSIC, frame: frameLight({ bg: '#fdf6e3', fg: '#3b2a1a', link: '#c27835', border: '#d4c4a8', muted: '#7a6550' }),
  },
  {
    id: 'slate', name: 'Slate', group: 'Mid', tagline: 'Mid grey-blue, indigo accent.',
    dark: true, swatch: { bg: '#1e293b', accent: '#818cf8' }, variants: CLASSIC, frame: frameDark({ bg: '#1e293b', fg: '#e2e8f0', link: '#818cf8', border: '#334155', muted: '#94a3b8' }),
  },
  {
    id: 'dusk', name: 'Dusk', group: 'Mid', tagline: 'Mid warm brown, amber-rose accent.',
    dark: true, swatch: { bg: '#2a1f1a', accent: '#e07b4f' }, variants: CLASSIC, frame: frameDark({ bg: '#2a1f1a', fg: '#e8d5c4', link: '#e07b4f', border: '#4a3728', muted: '#b09070' }),
  },
  {
    id: 'midnight', name: 'Midnight', group: 'Dark', tagline: 'Dark, blue accent.',
    dark: true, swatch: { bg: '#111827', accent: '#3b82f6' }, variants: CLASSIC, frame: frameDark({ bg: '#111827', fg: '#f9fafb', link: '#3b82f6', border: '#374151', muted: '#9ca3af' }),
  },
  {
    id: 'forest', name: 'Forest', group: 'Dark', tagline: 'Dark green, sage accent.',
    dark: true, swatch: { bg: '#0d1f0d', accent: '#4ade80' }, variants: CLASSIC, frame: frameDark({ bg: '#0d1f0d', fg: '#d4e8d4', link: '#4ade80', border: '#1a3a1a', muted: '#7aaa7a' }),
  },
  {
    id: 'neon', name: 'Neon', group: 'Vivid', tagline: 'Cyberpunk cyan-purple.',
    dark: true, swatch: { bg: '#0a0a1a', accent: '#06d6a0' }, variants: CLASSIC, frame: frameDark({ bg: '#0a0a1a', fg: '#e0e6ff', link: '#06d6a0', border: '#1e2248', muted: '#8890b8' }),
  },
  {
    id: 'aurora', name: 'Aurora', group: 'Vivid', tagline: 'Ethereal teal-purple.',
    dark: true, swatch: { bg: '#0d0d1a', accent: '#38bdf8' }, variants: CLASSIC, frame: frameDark({ bg: '#0d0d1a', fg: '#dce6f0', link: '#38bdf8', border: '#1a2040', muted: '#7888a8' }),
  },
  {
    id: 'industrial', name: 'Industrial', group: 'Tech', tagline: 'Orange-teal on dark grey.',
    dark: true, swatch: { bg: '#1a1a1a', accent: '#FF6B35' }, variants: CLASSIC, frame: frameDark({ bg: '#1a1a1a', fg: '#e8e8e8', link: '#FF6B35', border: '#333333', muted: '#777777' }),
  },
];

export const THEME_IDS: ThemeId[] = THEME_PACKS.map(p => p.id);
export const DARK_THEME_IDS: ThemeId[] = THEME_PACKS.filter(p => p.dark).map(p => p.id);

export function getThemePack(id: ThemeId): ThemePack {
  return THEME_PACKS.find(p => p.id === id) ?? THEME_PACKS[0];
}

/** Deterministic avatar tint so a sender keeps one colour across the list. */
const AVATAR_TINTS = [
  '#2c4373', '#3e6a5c', '#635bff', '#c2452d', '#5e6ad2',
  '#0f766e', '#8a4f7d', '#b45309', '#475569', '#1a2a4a',
];

export function avatarTint(seed: string): string {
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) | 0;
  return AVATAR_TINTS[Math.abs(h) % AVATAR_TINTS.length];
}

export function initialsOf(name: string): string {
  const parts = name.trim().split(/[\s._@-]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return name.slice(0, 2).toUpperCase();
}
