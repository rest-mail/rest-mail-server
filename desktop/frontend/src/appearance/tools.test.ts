import { beforeEach, describe, expect, it } from 'vitest';
import { handleUIRequest, type BridgeOptions } from '../bridge';
import { appearance } from './engine';

const options: BridgeOptions = { route: () => '/signin', navigate: () => {}, screenshot: async () => '' };
const request = (action: string, args?: Record<string, unknown>) => handleUIRequest({ id: 'theme-test', action, args }, options) as Promise<any>;
beforeEach(() => { localStorage.clear(); appearance.restore('desktop.test', 'tools@desktop.test'); });
describe('theme MCP bridge', () => {
  it('lists all presets and saved themes before sign-in and returns the selected tokens', async () => {
    expect((await request('themeList')).builtIn).toHaveLength(10);
    const changed = await request('themeSet', { theme: 'Midnight' });
    expect(changed).toMatchObject({ name: 'Midnight', selected: 'midnight', theme: { dark: true, fonts: { ui: 'Geist' } } });
    expect(await request('themeGet')).toEqual(changed);
    appearance.save('My midnight');
    expect((await request('themeList')).saved[0].name).toBe('My midnight');
    expect((await request('themeSet', { theme: 'My midnight' })).name).toBe('My midnight');
  });
  it('accepts named or ID-based remix specifications and zero-radius overrides', async () => {
    const changed = await request('themeSet', { remix: { colours: 'Canopy', typefaces: 'Letterpress', shape: 'concrete', overrides: { radius: 0, density: 44, accent: '#ffcc99', ui: 'Space Grotesk' } } });
    expect(changed.spec).toMatchObject({ colours: 'canopy', typefaces: 'letterpress', shape: 'concrete' });
    expect(changed.theme).toMatchObject({ name: 'Custom', colours: { sidebar: '#0d201c', accent: '#ffcc99' }, fonts: { display: 'DM Serif Display', ui: 'Space Grotesk' }, shape: { radius: 0, density: 44 } });
  });
  it('rejects unknown names, malformed specs and unsafe or out-of-range overrides without mutation', async () => {
    const before = appearance.get();
    for (const args of [{}, { theme: 'Missing' }, { theme: 'Midnight', remix: {} }, { remix: { colours: 'missing', typefaces: 'rest-mail', shape: 'rest-mail' } }, { remix: { colours: 'canopy' } }, { remix: { colours: 'canopy', typefaces: 'rest-mail', shape: 'rest-mail', overrides: { accent: 'url(evil)' } } }, { remix: { colours: 'canopy', typefaces: 'rest-mail', shape: 'rest-mail', overrides: { radius: 80 } } }]) await expect(request('themeSet', args)).rejects.toThrow();
    expect(appearance.get()).toBe(before);
  });
});
