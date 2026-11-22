import { appearance, normalizeSpec, resolveTheme, type Overrides, type RemixSpec } from './engine';
import { builtInThemes, themeByName } from './themes';

export function getTheme() {
  const state = appearance.get();
  const name = state.saved.find(theme => theme.id === state.selected)?.name ?? builtInThemes.find(theme => theme.id === state.selected)?.name ?? 'Custom';
  return { selected: state.selected, name, spec: state.spec, theme: { ...resolveTheme(state.spec), id: state.selected, name } };
}
export function listThemes() {
  return { builtIn: builtInThemes, saved: appearance.get().saved.map(theme => ({ ...theme, theme: resolveTheme(theme.spec) })) };
}
export function setTheme(args: Record<string, unknown>) {
  if (typeof args.theme === 'string' && args.theme && args.remix === undefined) appearance.select(args.theme);
  else if (args.theme === undefined && args.remix && typeof args.remix === 'object' && !Array.isArray(args.remix)) {
    const input = args.remix as Record<string, unknown>;
    for (const category of ['colours', 'typefaces', 'shape']) if (typeof input[category] !== 'string') throw new Error(`${category} must name a built-in theme`);
    const overrides = input.overrides ?? {};
    if (!overrides || typeof overrides !== 'object' || Array.isArray(overrides)) throw new Error('overrides must be an object');
    const spec: RemixSpec = { colours: themeByName(input.colours as string).id, typefaces: themeByName(input.typefaces as string).id, shape: themeByName(input.shape as string).id, overrides: overrides as Overrides };
    const normalized = normalizeSpec(spec);
    for (const [key, value] of Object.entries(overrides)) if (normalized.overrides[key as keyof Overrides] !== (key === 'accent' && typeof value === 'string' ? value.toLowerCase() : value)) throw new Error(`Invalid override: ${key}`);
    appearance.remix(spec);
  } else throw new Error('Supply exactly one theme name or remix specification');
  return getTheme();
}
