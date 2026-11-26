import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() }, Events: { On: vi.fn(), Emit: vi.fn() } }));
import { Call } from '@wailsio/runtime';
import { ThunderbirdImport, ThunderbirdImportSheet, formatImportBytes, type ImportCatalog, type ImportProgress } from './ThunderbirdImport';
import { useMailStore } from '@webmail/stores/mailStore';
import { useAuthStore } from '@webmail/stores/authStore';

const catalog: ImportCatalog = { profiles: [{ id: 'sample', name: 'Synthetic', folders: [
  { id: 'Mail/Local Folders/Inbox', account: 'Local Folders', name: 'Inbox', destination: 'INBOX', bytes: 1024, messages: 2, expunged: 0 },
  { id: 'Mail/Local Folders/Archives.sbd/2024', account: 'Local Folders', name: 'Archives/2024', destination: 'Archive/2024', bytes: 2048, messages: 1, expunged: 1 },
] }], scanning: false, running: true };
const idle: ImportProgress = { state: 'idle', selection: { profile_id: '', folders: [], account_id: 0 }, folder: '', messages: 0, total_messages: 0, bytes: 0, total_bytes: 0, imported: 0, duplicates: 0, expunged: 0, failed: 0, failures: [] };
let current: ImportProgress;

beforeEach(() => {
  current = structuredClone(idle);
  useMailStore.setState({ activeAccountId: 7, accounts: [{ id: 7, address: 'destination@example.test', display_name: 'Destination', is_primary: true }, { id: 2 ** 52 + 1, address: 'imap@example.test', display_name: 'IMAP', is_primary: false }] });
  useAuthStore.setState({ isAuthenticated: true });
  vi.spyOn(useMailStore.getState(), 'loadFolders').mockResolvedValue();
  vi.mocked(Call.ByName).mockReset().mockImplementation((method, ...args) => {
    const result = async () => {
    if (method === 'main.Imports.Profiles' || method === 'main.Imports.RefreshProfiles') return structuredClone(catalog);
    if (method === 'main.Imports.Status') { if (args[0]) current = { ...current, state: 'cancelled' }; return structuredClone(current); }
    if (method === 'main.Imports.Start') { current = { ...idle, state: 'running', selection: args[0] as ImportProgress['selection'], total_bytes: 3072, total_messages: 3 }; return structuredClone(current); }
      throw new Error('unexpected binding');
    };
    return result() as ReturnType<typeof Call.ByName>;
  });
});

describe('Thunderbird import sheet', () => {
  it('shows discovered folders, totals, mappings and running warning without preselecting mail', async () => {
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    expect(await screen.findByText('Thunderbird is running.')).toBeTruthy();
    expect(screen.queryByText('BRING YOUR MAIL WITH YOU')).toBeNull();
    expect(screen.getByText('→ Archive/2024')).toBeTruthy();
    expect((screen.getByTestId('thunderbird-start') as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText('0 folders selected')).toBeTruthy();
    expect(screen.queryByRole('option', { name: 'imap@example.test' })).toBeNull();
  });

  it('selects folders and starts a background import with only native identity inputs', async () => {
    vi.mocked(Call.ByName).mockResolvedValueOnce({ ...catalog, scanning: true });
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    fireEvent.click(await screen.findByRole('button', { name: 'Select all' }));
    expect(screen.getByText('2 folders selected')).toBeTruthy();
    expect(screen.getByText('3 messages · 3.0 KB')).toBeTruthy();
    fireEvent.click(screen.getByTestId('thunderbird-start'));
    await screen.findByText('Importing your mail');
    expect(Call.ByName).toHaveBeenCalledWith('main.Imports.Start', { profile_id: 'sample', folders: catalog.profiles[0].folders.map(folder => folder.id), account_id: 7 });
    expect(screen.getByRole('progressbar', { name: 'Import byte progress' })).toBeTruthy();
    expect(screen.getByTestId('thunderbird-destination').matches(':disabled')).toBe(true);
    fireEvent.click(screen.getByTestId('thunderbird-cancel'));
    expect(await screen.findByText('Import cancelled')).toBeTruthy();
    expect(Call.ByName).toHaveBeenCalledWith('main.Imports.Status', true);
    expect(screen.getByRole('button', { name: 'Import again' })).toBeTruthy();
    expect(screen.getByText(/existing copies are skipped by the server/)).toBeTruthy();
  });

  it('reopens saved selections and shows duplicate, failure and expunged summaries', async () => {
    current = { ...idle, state: 'interrupted', selection: { profile_id: 'sample', folders: [catalog.profiles[0].folders[0].id], account_id: 7 }, imported: 2, duplicates: 1, expunged: 1, failed: 1, failures: [{ folder: 'Inbox', message: 4, reason: 'Server refused message (HTTP 413).' }] };
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    await screen.findByText('Previous import interrupted');
    expect((screen.getByTestId('thunderbird-folder-Mail/Local Folders/Inbox') as HTMLInputElement).checked).toBe(true);
    expect(screen.getByText('1 expunged skipped')).toBeTruthy();
    expect(screen.getByText(/Server refused message/)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Import again' })).toBeTruthy();
  });

  it('polls live progress through completion', async () => {
    current = { ...idle, state: 'running', selection: { profile_id: 'sample', folders: [catalog.profiles[0].folders[0].id], account_id: 7 }, total_bytes: 1024, total_messages: 2 };
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    await screen.findByText('Importing your mail');
    current = { ...current, state: 'completed', messages: 2, imported: 2, bytes: 1024 };
    await waitFor(() => expect(screen.getByText('Import complete')).toBeTruthy(), { timeout: 2000 });
    expect(screen.getByText('100%')).toBeTruthy();
  });

  it('filters a large folder list without selecting hidden folders', async () => {
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    await screen.findByText('→ Archive/2024');
    fireEvent.change(screen.getByLabelText('Find Thunderbird folders'), { target: { value: '2024' } });
    expect(screen.queryByTestId('thunderbird-folder-Mail/Local Folders/Inbox')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Select matching' }));
    expect(screen.getByText('1 folder selected')).toBeTruthy();
    fireEvent.click(screen.getByTestId('thunderbird-start'));
    await screen.findByText('Importing your mail');
    expect(Call.ByName).toHaveBeenCalledWith('main.Imports.Start', { profile_id: 'sample', folders: ['Mail/Local Folders/Archives.sbd/2024'], account_id: 7 });
  });

  it('observes an import started by MCP while the sheet is already open', async () => {
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    await screen.findByText('0 folders selected');
    current = { ...idle, state: 'running', selection: { profile_id: 'sample', folders: ['Mail/Local Folders/Archives.sbd/2024'], account_id: 7 }, total_bytes: 2048, total_messages: 1 };
    await waitFor(() => expect(screen.getByText('Importing your mail')).toBeTruthy(), { timeout: 2000 });
    expect(screen.getByText('1 folder selected')).toBeTruthy();
  });

  it('opens from desktop action and closes without cancelling background work', async () => {
    render(<ThunderbirdImport />);
    expect(screen.queryByRole('dialog')).toBeNull();
    fireEvent(window, new Event('desktop:import-thunderbird'));
    await screen.findByRole('dialog', { name: 'Import from Thunderbird' });
    fireEvent.click(screen.getByRole('button', { name: 'Close import sheet' }));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(Call.ByName).not.toHaveBeenCalledWith('main.Imports.Status', true);
  });

  it('reports discovery errors without echoing upstream text', async () => {
    vi.mocked(Call.ByName).mockRejectedValue(new Error('private upstream text'));
    render(<ThunderbirdImportSheet onClose={vi.fn()} />);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('Could not discover');
    expect(alert.textContent).not.toContain('private');
  });

  it('formats bytes without rounding multi-gigabyte profiles to integers', () => {
    expect(formatImportBytes(0)).toBe('0 B');
    expect(formatImportBytes(1024)).toBe('1.0 KB');
    expect(formatImportBytes(1024 ** 2)).toBe('1.0 MB');
    expect(formatImportBytes(20 * 1024 ** 3)).toBe('20.0 GB');
  });
});
