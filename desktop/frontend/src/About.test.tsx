import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() } }));
import { Call } from '@wailsio/runtime';
import { AboutRelease, formatBuilt, loadRelease } from './About';

describe('About this build', () => {
  it('asks the Go AppInfo service for the running release', async () => {
    vi.mocked(Call.ByName).mockResolvedValue({ version: '0.8.1', commit: 'a'.repeat(40), builtAt: '2026-11-22T08:29:13Z' });
    await expect(loadRelease()).resolves.toMatchObject({ version: '0.8.1' });
    expect(Call.ByName).toHaveBeenCalledWith('main.AppInfo.Release');
  });

  it('shows the semantic version, the short commit and the build date', async () => {
    const load = () => Promise.resolve({ version: '0.8.1', commit: '2f616a5278f6514a6159bd6cd046d653841c3242', builtAt: '2026-11-22T08:29:13Z' });
    render(<AboutRelease load={load} />);
    const about = await screen.findByTestId('about-release');
    expect(about.textContent).toContain('REST Mail 0.8.1');
    expect(about.textContent).toContain('Build 2f616a5');
    expect(about.textContent).toContain(formatBuilt('2026-11-22T08:29:13Z'));
  });

  it('marks a build made from uncommitted changes', async () => {
    const load = () => Promise.resolve({ version: '0.8.2-dirty', commit: 'b'.repeat(40) + '-dirty', builtAt: 'unknown' });
    render(<AboutRelease load={load} />);
    const about = await screen.findByTestId('about-release');
    expect(about.textContent).toContain('REST Mail 0.8.2-dirty');
    expect(about.textContent).toContain('Build bbbbbbb (modified)');
    expect(about.textContent).toContain('unknown');
  });

  it('shows nothing when the release cannot be read', async () => {
    const { container } = render(<AboutRelease load={() => Promise.reject(new Error('no runtime'))} />);
    await Promise.resolve();
    expect(container.textContent).toBe('');
  });
});
