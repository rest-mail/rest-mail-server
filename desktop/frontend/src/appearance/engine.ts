import { useSyncExternalStore } from 'react';
import { fontFamilies, fontStack, type FontFamily } from './fonts';
import { builtInThemes, contrastInk, defaultTheme, themeByName, type Shape, type Theme, type Typefaces } from './themes';

export interface Overrides { accent?: string; display?: FontFamily; ui?: FontFamily; reading?: FontFamily; mono?: FontFamily; radius?: number; density?: number }
export interface RemixSpec { colours: string; typefaces: string; shape: string; overrides: Overrides }
export interface SavedTheme { id: string; name: string; spec: RemixSpec }
export interface AppearanceState { spec: RemixSpec; saved: SavedTheme[]; selected: string }
const baseSpec = (): RemixSpec => ({ colours: defaultTheme.id, typefaces: defaultTheme.id, shape: defaultTheme.id, overrides: {} });
const defaults = (): AppearanceState => ({ spec: baseSpec(), saved: [], selected: defaultTheme.id });
let state = defaults();
let storageKey = 'restmail:appearance:guest';
let settingsOpen = false;
const listeners = new Set<() => void>();
const settingsListeners = new Set<() => void>();
const hexColour = /^#[0-9a-f]{6}$/i;

function normalizeOverrides(value: unknown): Overrides {
  const source = value && typeof value === 'object' ? value as Record<string, unknown> : {};
  const result: Overrides = {};
  if (typeof source.accent === 'string' && hexColour.test(source.accent)) result.accent = source.accent.toLowerCase();
  for (const role of ['display', 'ui', 'reading', 'mono'] as const) if (fontFamilies.includes(source[role] as FontFamily)) result[role] = source[role] as FontFamily;
  for (const [key, minimum, maximum] of [['radius', 0, 24], ['density', 28, 48]] as const) {
    if (typeof source[key] === 'number' && Number.isFinite(source[key])) result[key] = Math.max(minimum, Math.min(maximum, Math.round(source[key])));
  }
  return result;
}

export function normalizeSpec(value: unknown): RemixSpec {
  const source = value && typeof value === 'object' ? value as Partial<RemixSpec> : {};
  const valid = (name: unknown) => typeof name === 'string' && builtInThemes.some(theme => theme.id === name) ? name : defaultTheme.id;
  return { colours: valid(source.colours), typefaces: valid(source.typefaces), shape: valid(source.shape), overrides: normalizeOverrides(source.overrides) };
}

export function resolveTheme(spec: RemixSpec): Theme {
  const colours = themeByName(spec.colours), typefaces = themeByName(spec.typefaces), shape = themeByName(spec.shape);
  const overrides = normalizeOverrides(spec.overrides);
  const fonts: Typefaces = { ...typefaces.fonts };
  for (const role of ['display', 'ui', 'reading', 'mono'] as const) if (overrides[role]) fonts[role] = overrides[role];
  const geometry: Shape = { ...shape.shape, ...(overrides.radius === undefined ? {} : { radius: overrides.radius }), ...(overrides.density === undefined ? {} : { density: overrides.density }) };
  return {
    ...colours, id: 'custom', name: 'Custom', fonts, shape: geometry,
    colours: { ...colours.colours, ...(overrides.accent ? { accent: overrides.accent, accentInk: contrastInk(overrides.accent) } : {}) },
  };
}

export function themeProperties(theme: Theme): Record<string, string> {
  const colours = Object.fromEntries(Object.entries(theme.colours).map(([key, value]) => [`--theme-${key.replace(/[A-Z]/g, letter => `-${letter.toLowerCase()}`)}`, value]));
  return {
    ...colours, '--theme-texture': theme.texture,
    '--theme-display': fontStack(theme.fonts.display), '--desktop-font': fontStack(theme.fonts.ui), '--theme-reading': fontStack(theme.fonts.reading), '--desktop-mono': fontStack(theme.fonts.mono),
    '--radius': `${theme.shape.radius}px`, '--desktop-row-height': `${theme.shape.density}px`, '--theme-scale': String(theme.shape.scale),
    '--desktop-caption': `${11 * theme.shape.scale}px`, '--desktop-small': `${12 * theme.shape.scale}px`, '--desktop-body': `${13 * theme.shape.scale}px`, '--desktop-title': `${15 * theme.shape.scale}px`, '--desktop-heading': `${22 * theme.shape.scale}px`,
    '--ink': theme.colours.ink, '--deep': theme.colours.selection, '--signal': theme.colours.accent, '--mist': theme.colours.sidebarMuted,
    '--sheet': theme.colours.sheet, '--well': theme.colours.list, '--hair': `color-mix(in srgb, ${theme.colours.border} 30%, ${theme.colours.sheet})`,
    '--foreground': theme.colours.ink, '--background': theme.colours.reader, '--card': theme.colours.sheet, '--card-foreground': theme.colours.ink,
    '--primary': theme.colours.selection, '--primary-foreground': theme.colours.selectionInk, '--border': theme.colours.border, '--input': theme.colours.border,
    '--muted': theme.colours.list, '--muted-foreground': theme.colours.muted, '--secondary': theme.colours.list, '--secondary-foreground': theme.colours.ink,
    '--popover': theme.colours.sheet, '--popover-foreground': theme.colours.ink, '--accent': theme.colours.selection, '--accent-foreground': theme.colours.selectionInk,
    '--destructive': theme.colours.danger, '--success': theme.colours.success, '--go': theme.colours.success, '--ring': theme.colours.selection,
    '--desktop-sidebar-hover': `color-mix(in srgb, ${theme.colours.sidebarInk} 10%, ${theme.colours.sidebar})`,
    '--desktop-sidebar-selected': theme.colours.accent,
  };
}

function apply() {
  if (typeof document === 'undefined') return;
  const theme = resolveTheme(state.spec);
  for (const [key, value] of Object.entries(themeProperties(theme))) document.documentElement.style.setProperty(key, value);
  document.documentElement.dataset.desktopTheme = state.selected;
  document.documentElement.style.colorScheme = theme.dark ? 'dark' : 'light';
}
function publish(persist = true) {
  apply();
  if (persist) try {
    localStorage.setItem(storageKey, JSON.stringify(state));
    localStorage.setItem('restmail:appearance:last', JSON.stringify(state));
  } catch {}
  listeners.forEach(listener => listener());
}
function read(key: string): AppearanceState {
  try {
    const value = JSON.parse(localStorage.getItem(key) || 'null');
    if (!value || typeof value !== 'object') return defaults();
    const saved: SavedTheme[] = Array.isArray(value.saved) ? value.saved.filter((item: SavedTheme) => item && typeof item.id === 'string' && item.id.startsWith('saved-') && typeof item.name === 'string').slice(0, 100).map((item: SavedTheme) => ({ id: item.id, name: item.name.slice(0, 60), spec: normalizeSpec(item.spec) })) : [];
    const selected = typeof value.selected === 'string' && (value.selected === 'custom' || builtInThemes.some(theme => theme.id === value.selected) || saved.some(theme => theme.id === value.selected)) ? value.selected : defaultTheme.id;
    return { spec: normalizeSpec(value.spec), saved, selected };
  } catch { return defaults(); }
}

export const appearance = {
  get: () => state,
  subscribe: (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; },
  init: () => { state = read('restmail:appearance:last'); publish(false); },
  restore: (server: string, email: string) => {
    if (!email) {
      storageKey = 'restmail:appearance:guest';
      state = { ...state, saved: [], selected: builtInThemes.some(theme => theme.id === state.selected) ? state.selected : 'custom' };
      publish(false); return;
    }
    storageKey = `restmail:appearance:${encodeURIComponent(server)}:${encodeURIComponent(email.toLowerCase())}`;
    state = read(storageKey); publish(false);
  },
  select: (name: string) => {
    const saved = state.saved.find(theme => theme.id === name || theme.name.toLowerCase() === name.toLowerCase());
    if (saved) state = { ...state, selected: saved.id, spec: normalizeSpec(saved.spec) };
    else { const theme = themeByName(name); state = { ...state, selected: theme.id, spec: { colours: theme.id, typefaces: theme.id, shape: theme.id, overrides: {} } }; }
    publish();
  },
  remix: (spec: Partial<RemixSpec>) => { state = { ...state, selected: 'custom', spec: normalizeSpec({ ...state.spec, ...spec }) }; publish(); },
  override: (overrides: Overrides) => { state = { ...state, selected: 'custom', spec: { ...state.spec, overrides: normalizeOverrides({ ...state.spec.overrides, ...overrides }) } }; publish(); },
  reset: () => { const theme = state.spec.colours; appearance.select(theme); },
  surprise: () => {
    const pick = () => builtInThemes[Math.floor(Math.random() * builtInThemes.length)].id;
    const colours = pick();
    const alternatives = builtInThemes.filter(theme => theme.id !== colours);
    const typefaces = alternatives.length ? alternatives[Math.floor(Math.random() * alternatives.length)].id : colours;
    appearance.remix({ colours, typefaces, shape: pick(), overrides: {} });
  },
  save: (name: string) => {
    const title = name.trim().slice(0, 60);
    if (!title) throw new Error('Give your theme a name.');
    if (state.saved.length >= 100) throw new Error('You can save up to 100 themes. Delete one to make room.');
    const item: SavedTheme = { id: `saved-${crypto.randomUUID()}`, name: title, spec: normalizeSpec(state.spec) };
    state = { ...state, selected: item.id, saved: [...state.saved, item] }; publish(); return item;
  },
  remove: (id: string) => { state = { ...state, saved: state.saved.filter(theme => theme.id !== id), selected: state.selected === id ? 'custom' : state.selected }; publish(); },
};

export const appearanceSettings = {
  get: () => settingsOpen,
  subscribe: (listener: () => void) => { settingsListeners.add(listener); return () => { settingsListeners.delete(listener); }; },
  open: () => { settingsOpen = true; settingsListeners.forEach(listener => listener()); },
  close: () => { settingsOpen = false; settingsListeners.forEach(listener => listener()); },
};
export function useAppearance() { return useSyncExternalStore(appearance.subscribe, appearance.get); }
export function useAppearanceSettings() { return useSyncExternalStore(appearanceSettings.subscribe, appearanceSettings.get); }
