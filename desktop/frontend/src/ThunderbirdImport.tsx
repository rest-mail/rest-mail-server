import { Call } from '@wailsio/runtime';
import { useEffect, useRef, useState } from 'react';
import { useMailStore } from '@webmail/stores/mailStore';
import { useAuthStore } from '@webmail/stores/authStore';
import './thunderbird-import.css';

export interface ImportFolder { id: string; account: string; name: string; destination: string; bytes: number; messages: number; expunged: number; problem?: string }
export interface ImportProfile { id: string; name: string; folders: ImportFolder[] }
export interface ImportCatalog { profiles: ImportProfile[]; scanning: boolean; running: boolean; problem?: string }
export interface ImportSelection { profile_id: string; folders: string[]; account_id: number }
export interface ImportProgress { state: string; selection: ImportSelection; folder: string; messages: number; total_messages: number; bytes: number; total_bytes: number; imported: number; duplicates: number; expunged: number; failed: number; failures: { folder: string; message?: number; reason: string }[] }

export const thunderbird = {
  profiles: () => Call.ByName('main.Imports.Profiles') as Promise<ImportCatalog>,
  refreshProfiles: () => Call.ByName('main.Imports.RefreshProfiles') as Promise<ImportCatalog>,
  start: (selection: ImportSelection) => Call.ByName('main.Imports.Start', selection) as Promise<ImportProgress>,
  status: (cancel = false) => Call.ByName('main.Imports.Status', cancel) as Promise<ImportProgress>,
};

export function formatImportBytes(value: number): string {
  if (value < 1024) return `${value} B`;
  const unit = value < 1024 ** 2 ? 1 : value < 1024 ** 3 ? 2 : 3;
  return `${(value / 1024 ** unit).toFixed(1)} ${['B', 'KB', 'MB', 'GB'][unit]}`;
}

export function ThunderbirdImport() {
  const [open, setOpen] = useState(false);
  const signedIn = useAuthStore(state => state.isAuthenticated);
  useEffect(() => { const show = () => setOpen(true); window.addEventListener('desktop:import-thunderbird', show); return () => window.removeEventListener('desktop:import-thunderbird', show); }, []);
  return open && signedIn ? <ThunderbirdImportSheet onClose={() => setOpen(false)} /> : null;
}

export function ThunderbirdImportSheet({ onClose }: { onClose: () => void }) {
  const accounts = useMailStore(state => state.accounts).filter(account => account.id > 0 && account.id < 2 ** 32);
  const activeAccount = useMailStore(state => state.activeAccountId);
  const [catalog, setCatalog] = useState<ImportCatalog | null>(null);
  const [profileID, setProfileID] = useState('');
  const [selected, setSelected] = useState<string[]>([]);
  const [filter, setFilter] = useState('');
  const [accountID, setAccountID] = useState(accounts.find(account => account.id === activeAccount)?.id ?? accounts[0]?.id ?? 0);
  const [status, setStatus] = useState<ImportProgress | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const dialog = useRef<HTMLDivElement>(null);
  const running = status?.state === 'running' || status?.state === 'cancelling';

  useEffect(() => {
    let alive = true;
    const initialFocus = document.activeElement as HTMLElement | null;
    dialog.current?.focus();
    void Promise.all([thunderbird.refreshProfiles(), thunderbird.status()]).then(([found, progress]) => {
      if (!alive) return;
      setCatalog(found); setStatus(progress);
      const previous = found.profiles.find(profile => profile.id === progress.selection?.profile_id);
      setProfileID(previous?.id ?? found.profiles[0]?.id ?? '');
      if (previous) setSelected(progress.selection.folders.filter(id => previous.folders.some(folder => folder.id === id)));
      if (accounts.some(account => account.id === progress.selection?.account_id)) setAccountID(progress.selection.account_id);
    }).catch(() => { if (alive) setError('Could not discover Thunderbird profiles. Check file permissions and try again.'); });
    return () => { alive = false; initialFocus?.focus(); };
  }, []);

  useEffect(() => {
    let alive = true;
    let pending = false;
    const timer = window.setInterval(async () => {
      if (pending) return; pending = true;
      try {
        const [found, progress] = await Promise.all([catalog?.scanning ? thunderbird.profiles() : Promise.resolve(null), thunderbird.status()]);
        if (alive) {
          if (found) setCatalog(found);
          setStatus(progress);
          if (progress.state === 'running' || progress.state === 'cancelling') { setProfileID(progress.selection.profile_id); setSelected(progress.selection.folders); setAccountID(progress.selection.account_id); }
          else if (running) void useMailStore.getState().loadFolders();
        }
      } catch { if (alive) setError('Could not read import progress. Reopen this sheet to reconnect; the import may still be running.'); }
      finally { pending = false; }
    }, 500);
    return () => { alive = false; window.clearInterval(timer); };
  }, [catalog?.scanning, running]);

  const profile = catalog?.profiles.find(item => item.id === profileID);
  const folders = profile?.folders ?? [];
  const selectedFolders = folders.filter(folder => selected.includes(folder.id));
  const countingSelection = selectedFolders.some(folder => folder.messages < 0);
  const totalBytes = selectedFolders.reduce((sum, folder) => sum + folder.bytes, 0);
  const totalMessages = selectedFolders.reduce((sum, folder) => sum + Math.max(0, folder.messages), 0);
  const visibleFolders = folders.filter(folder => `${folder.name} ${folder.account}`.toLowerCase().includes(filter.toLowerCase()));
  const groups = [...new Set(visibleFolders.map(folder => folder.account))];
  const toggle = (ids: string[]) => setSelected(current => ids.every(id => current.includes(id)) ? current.filter(id => !ids.includes(id)) : [...new Set([...current, ...ids])]);
  const begin = async () => {
    setBusy(true); setError('');
    try { setStatus(await thunderbird.start({ profile_id: profileID, folders: selected, account_id: accountID })); }
    catch { setError('Could not start import. Sign in to a REST Mail account, select folders, and try again.'); }
    finally { setBusy(false); }
  };
  const cancel = async () => { try { setStatus(await thunderbird.status(true)); } catch { setError('Could not cancel. Reopen this sheet to check the current import.'); } };
  const progress = status?.total_bytes ? Math.min(100, status.bytes / status.total_bytes * 100) : 0;
  const finished = status && !['idle', 'running', 'cancelling'].includes(status.state);

  return <div className="thunderbird-backdrop" onKeyDown={event => {
    if (event.key === 'Escape') { event.stopPropagation(); onClose(); }
    if (event.key === 'Tab') {
      const focusable = dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), select:not(:disabled), input:not(:disabled), summary');
      if (!focusable?.length) return;
      if (event.shiftKey && (document.activeElement === focusable[0] || document.activeElement === dialog.current)) { event.preventDefault(); focusable[focusable.length - 1].focus(); }
      if (!event.shiftKey && document.activeElement === focusable[focusable.length - 1]) { event.preventDefault(); focusable[0].focus(); }
    }
  }}>
    <div ref={dialog} tabIndex={-1} className="thunderbird-sheet" role="dialog" aria-modal="true" aria-labelledby="thunderbird-title" data-testid="thunderbird-sheet">
      <header><div><h1 id="thunderbird-title">Import from Thunderbird</h1><p>Copy your history into REST Mail. Your original files stay untouched.</p></div><button className="thunderbird-close" aria-label="Close import sheet" onClick={onClose}>×</button></header>
      <div className="thunderbird-content">
        <div className={`thunderbird-notice ${catalog?.running ? 'thunderbird-warning' : ''}`} role={catalog?.running ? 'alert' : 'note'}><strong>{catalog?.running ? 'Thunderbird is running.' : 'Before you begin'}</strong> Quit Thunderbird before importing: active mailbox files may be mid-write. IMAP caches contain only mail downloaded for offline use.</div>
        {error && <p role="alert" className="thunderbird-error">{error}</p>}
        {catalog?.problem && <p role="alert">{catalog.problem}</p>}
        {!catalog && !error && <p role="status">Finding Thunderbird profiles…</p>}
        {catalog && !catalog.profiles.length && <p>No Thunderbird profiles found. Open Thunderbird on this computer and download your mail for offline use first.</p>}
        <fieldset disabled={running || busy} className={`thunderbird-selection ${running ? 'thunderbird-selection-compact' : ''}`}>
          <div className="thunderbird-selectors"><label>Thunderbird profile<select data-testid="thunderbird-profile" value={profileID} onChange={event => { setProfileID(event.target.value); setSelected([]); }}><option value="" disabled>Select a profile</option>{catalog?.profiles.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><label>Destination REST Mail account<select data-ui-sensitive data-testid="thunderbird-destination" value={accountID} onChange={event => setAccountID(Number(event.target.value))}>{!accounts.length && <option value={0}>Sign in to a REST Mail server</option>}{accounts.map(account => <option key={account.id} value={account.id}>{account.address}</option>)}</select></label></div>
          <div className="thunderbird-folder-heading"><strong>Choose folders</strong><span>{catalog?.scanning ? 'Counting messages…' : `${folders.length} folders detected`}</span><button onClick={() => toggle(visibleFolders.map(folder => folder.id))} disabled={!visibleFolders.length}>{visibleFolders.length > 0 && visibleFolders.every(folder => selected.includes(folder.id)) ? 'Deselect all' : filter ? 'Select matching' : 'Select all'}</button></div>
          <input className="thunderbird-filter" data-testid="thunderbird-filter" aria-label="Find Thunderbird folders" placeholder="Find a folder…" value={filter} onChange={event => setFilter(event.target.value)} />
          <div className="thunderbird-folders">{groups.map(account => {
            const accountFolders = visibleFolders.filter(folder => folder.account === account);
            return <details open key={account} className="thunderbird-account"><summary>{account}<span>{formatImportBytes(accountFolders.reduce((sum, folder) => sum + folder.bytes, 0))}</span></summary><label className="thunderbird-account-check"><input type="checkbox" aria-label={`Select all ${account}`} checked={accountFolders.every(folder => selected.includes(folder.id))} onChange={() => toggle(accountFolders.map(folder => folder.id))} />All folders in this account</label>{accountFolders.map(folder => <label className="thunderbird-folder" key={folder.id} style={{ paddingLeft: 16 + (folder.name.split('/').length - 1) * 12 }}><input data-testid={`thunderbird-folder-${folder.id}`} type="checkbox" checked={selected.includes(folder.id)} onChange={() => toggle([folder.id])} /><span title={folder.problem ?? `${folder.name} → ${folder.destination}`}>{folder.name}<small>→ {folder.destination}{folder.problem ? ' · count unavailable' : ''}</small></span><span className="thunderbird-folder-count">{folder.messages < 0 ? '…' : folder.messages.toLocaleString()}<small>{formatImportBytes(folder.bytes)}</small></span></label>)}</details>;
          })}</div>
        </fieldset>
        {(running || finished) && status && <section className="thunderbird-progress" aria-live="polite" data-testid="thunderbird-progress"><div className="thunderbird-progress-heading"><strong>{running ? status.state === 'cancelling' ? 'Cancelling…' : 'Importing your mail' : status.state === 'completed' ? 'Import complete' : status.state === 'cancelled' ? 'Import cancelled' : status.state === 'interrupted' ? 'Previous import interrupted' : 'Import finished with errors'}</strong><span>{Math.round(progress)}%</span></div><progress aria-label="Import byte progress" max={100} value={progress} /><div className="thunderbird-progress-detail"><span>{status.messages.toLocaleString()}{status.total_messages ? ` / ${status.total_messages.toLocaleString()}` : ''} messages · {status.folder}</span><span>{formatImportBytes(status.bytes)} / {formatImportBytes(status.total_bytes)}</span></div><div className="thunderbird-summary"><span><strong>{status.imported}</strong> imported</span><span><strong>{status.duplicates}</strong> duplicates skipped</span><span><strong>{status.failed}</strong> failed</span>{status.expunged > 0 && <span>{status.expunged} expunged skipped</span>}</div>{status.failures.length > 0 && <details><summary>Failure reasons ({status.failed})</summary><ul>{status.failures.map((failure, index) => <li key={index}>{failure.folder}{failure.message ? ` · message ${failure.message}` : ''}: {failure.reason}</li>)}</ul></details>}<p>Progress is saved locally. Run the same selection again to resume; existing copies are skipped by the server.</p></section>}
      </div>
      <footer><div><strong>{selected.length} {selected.length === 1 ? 'folder' : 'folders'} selected</strong><span>{countingSelection ? 'Counting…' : `${totalMessages.toLocaleString()} messages`} · {formatImportBytes(totalBytes)}</span></div><div className="thunderbird-actions"><button onClick={onClose}>{running ? 'Run in background' : 'Close'}</button>{running ? <button className="thunderbird-primary" data-testid="thunderbird-cancel" onClick={() => void cancel()} disabled={status?.state === 'cancelling'}>Cancel import</button> : <button className="thunderbird-primary" data-testid="thunderbird-start" disabled={busy || !selected.length || !accountID} onClick={() => void begin()}>{busy ? 'Starting…' : finished ? 'Import again' : 'Import selected folders'}</button>}</div></footer>
    </div>
  </div>;
}
