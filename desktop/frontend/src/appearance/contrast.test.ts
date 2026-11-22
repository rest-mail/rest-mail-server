import { describe, expect, it } from 'vitest';
import { builtInThemes, contrast } from './themes';
import { resolveTheme } from './engine';

describe.each(builtInThemes)('$name contrast contract (WCAG AA)', theme => {
  const palette = theme.colours;
  for (const surface of ['list', 'reader', 'sheet'] as const) {
    for (const text of ['ink', 'muted', 'danger', 'success', 'selection'] as const) {
      it(`${text} / ${surface} ≥ 4.5:1`, () => expect(contrast(palette[text], palette[surface]), `${theme.name}: ${text} on ${surface}`).toBeGreaterThanOrEqual(4.5));
    }
    it(`control border / ${surface} ≥ 3:1`, () => expect(contrast(palette.border, palette[surface])).toBeGreaterThanOrEqual(3));
  }
  for (const [text, surface] of [['sidebarInk', 'sidebar'], ['sidebarMuted', 'sidebar'], ['accentInk', 'accent'], ['selectionInk', 'selection'], ['badgeInk', 'badge']] as const) {
    it(`${text} / ${surface} ≥ 4.5:1`, () => expect(contrast(palette[text], palette[surface])).toBeGreaterThanOrEqual(4.5));
  }
  it('unread indicator / list ≥ 3:1', () => expect(contrast(palette.unread, palette.list)).toBeGreaterThanOrEqual(3));
  it('brace / sidebar ≥ 3:1', () => expect(contrast(palette.brace, palette.sidebar)).toBeGreaterThanOrEqual(3));
});

it('keeps every palette/type/shape combination independent across all 1,000 remixes', () => {
  for (const colours of builtInThemes) for (const typefaces of builtInThemes) for (const shape of builtInThemes) {
    const theme = resolveTheme({ colours: colours.id, typefaces: typefaces.id, shape: shape.id, overrides: {} });
    expect(theme.colours).toEqual(colours.colours);
    expect(theme.fonts).toEqual(typefaces.fonts);
    expect(theme.shape).toEqual(shape.shape);
  }
});
