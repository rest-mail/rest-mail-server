import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { resolve } from 'node:path';
import { fontFamilies } from './fonts';

const directory = resolve('public/fonts');
const manifest = JSON.parse(readFileSync(resolve(directory, 'manifest.json'), 'utf8')) as { family: string; upstream_family: string; license: string; commit: string; assets: { file: string; bytes: number; sha256: string }[] }[];
describe('offline desktop fonts', () => {
  it('bundles twenty licensed families and every declared face without runtime CDN references', () => {
    expect(manifest.map(item => item.family)).toEqual([...fontFamilies]);
    const css = readFileSync('src/appearance/fonts.css', 'utf8');
    expect(css).not.toMatch(/https?:\/\//);
    for (const item of manifest) {
      expect(item.commit).toMatch(/^[0-9a-f]{40}$/);
      expect(['SIL OFL 1.1', 'Apache 2.0']).toContain(item.license);
      const slug = item.upstream_family.toLowerCase().replace(/ /g, '');
      const license = readFileSync(resolve(directory, `${slug}-${item.license === 'Apache 2.0' ? 'LICENSE' : 'OFL'}.txt`), 'utf8');
      expect(license).toContain(item.license === 'Apache 2.0' ? 'Apache License' : 'SIL OPEN FONT LICENSE');
      expect(css).toContain(`font-family: "${item.family}"`);
      for (const asset of item.assets) {
        const bytes = readFileSync(resolve(directory, asset.file));
        expect(bytes.toString('ascii', 0, 4)).toBe('wOF2');
        expect(bytes.byteLength).toBe(asset.bytes);
        expect(createHash('sha256').update(bytes).digest('hex')).toBe(asset.sha256);
        expect(css).toContain(`/fonts/${asset.file}`);
      }
    }
    expect(manifest.flatMap(item => item.assets).reduce((size, asset) => size + asset.bytes, 0)).toBeLessThan(2 * 1024 * 1024);
  });
});
