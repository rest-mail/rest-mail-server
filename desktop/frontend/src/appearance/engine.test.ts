import { beforeEach, describe, expect, it } from 'vitest';
import { appearance, normalizeSpec, resolveTheme, themeProperties } from './engine';
import { contrast, defaultTheme } from './themes';

beforeEach(() => { localStorage.clear(); appearance.restore('test.invalid', 'one@test.invalid'); });
describe('desktop appearance engine', () => {
  it('applies colours, type, scale, density and shape instantly without a reload', () => {
    appearance.select('REST Mail');
    expect(document.documentElement.style.getPropertyValue('--theme-sidebar')).toBe('#1a2a4a');
    expect(document.documentElement.style.getPropertyValue('--desktop-font')).toContain('Instrument Sans');
    appearance.override({ accent: '#ff0088', ui: 'Parcel Mono', radius: 0, density: 48 });
    expect(document.documentElement.style.getPropertyValue('--signal')).toBe('#ff0088');
    expect(document.documentElement.style.getPropertyValue('--radius')).toBe('0px');
    expect(document.documentElement.style.getPropertyValue('--desktop-row-height')).toBe('48px');
    expect(contrast(resolveTheme(appearance.get().spec).colours.accentInk, '#ff0088')).toBeGreaterThanOrEqual(4.5);
    appearance.override({ accent: '#000000' });
    const colours = resolveTheme(appearance.get().spec).colours;
    expect(contrast(colours.sidebarSelection, colours.sidebar)).toBeGreaterThanOrEqual(3);
    expect(contrast(colours.sidebarSelectionInk, colours.sidebarSelection)).toBeGreaterThanOrEqual(4.5);
  });
  it('isolates user and host preferences, restores saved remixes, and retains sign-in appearance', () => {
    appearance.override({ reading: 'Newsreader', radius: 17 });
    const saved = appearance.save('Sunday letters');
    appearance.restore('test.invalid', 'two@test.invalid');
    expect(appearance.get().saved).toEqual([]);
    appearance.restore('other.invalid', 'one@test.invalid');
    expect(appearance.get().spec.overrides).toEqual({});
    appearance.restore('test.invalid', 'one@test.invalid');
    expect(appearance.get().selected).toBe(saved.id);
    appearance.init();
    expect(appearance.get().spec.overrides.reading).toBe('Newsreader');
    appearance.restore('test.invalid', '');
    expect(appearance.get().selected).toBe('custom');
    expect(appearance.get().saved).toEqual([]);
    appearance.restore('test.invalid', 'one@test.invalid');
    appearance.remove(saved.id);
    expect(appearance.get().saved).toEqual([]);
    expect(appearance.get().selected).toBe('custom');
  });
  it('rejects unsafe style values, clamps geometry, and recovers corrupt storage', () => {
    expect(normalizeSpec({ colours: 'missing', overrides: { accent: 'url(evil)', ui: 'Injected', radius: 100, density: -4 } })).toEqual({ colours: 'rest-mail', typefaces: 'rest-mail', shape: 'rest-mail', overrides: { radius: 24, density: 28 } });
    localStorage.setItem('restmail:appearance:last', '{broken');
    expect(() => appearance.init()).not.toThrow();
    expect(appearance.get().selected).toBe(defaultTheme.id);
  });
  it('resets all remix categories and overrides to its colour theme', () => {
    appearance.override({ accent: '#000000', ui: 'Syne', radius: 23 });
    appearance.reset();
    expect(appearance.get().selected).toBe('rest-mail');
    expect(appearance.get().spec.overrides).toEqual({});
    expect(themeProperties(resolveTheme(appearance.get().spec))['--theme-display']).toContain('Bricolage Grotesque');
    expect(() => appearance.save('  ')).toThrow('name');
    expect(() => appearance.select('missing')).toThrow('Unknown theme');
  });
});
