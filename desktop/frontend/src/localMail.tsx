import { useEffect, useState, type ReactNode } from 'react';
import { Call } from '@wailsio/runtime';
import { useMailStore } from '@webmail/stores/mailStore';
import { useAuthStore } from '@webmail/stores/authStore';
import type { MessageSummary } from '@webmail/types';
import './local-mail.css';

export interface IndexStatus { account_id: number; messages: number; bodies: number; bytes: number; complete: boolean; offline: boolean; last_sync: string }
export interface SearchHit extends MessageSummary { snippet?: string; source?: string }
export interface IndexResponse { path: string; source: string; offline: boolean; result?: { data?: SearchHit[]; complete?: boolean } }
export const indexResponseEvent = 'desktop:index-response';
export const localMail = {
  status: (account: number) => Call.ByName('main.LocalMail.Status', account) as Promise<IndexStatus>,
  reindex: (account: number) => Call.ByName('main.LocalMail.Reindex', account) as Promise<void>,
  signOut: (deleteData: boolean) => Call.ByName('main.LocalMail.SignOut', deleteData) as Promise<void>,
};

export function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

function useIndexStatus() {
  const signedIn = useAuthStore(state => state.isAuthenticated);
  const account = useMailStore(state => state.activeAccountId);
  const [status, setStatus] = useState<IndexStatus | null>(null);
  useEffect(() => {
    setStatus(null);
    if (!signedIn || !account) return;
    let active = true;
    const refresh = async () => { try { const next = await localMail.status(account); if (active && next?.account_id === account) setStatus(next); } catch {} };
    void refresh();
    const timer = window.setInterval(() => { void refresh(); }, 3000);
    return () => { active = false; window.clearInterval(timer); };
  }, [signedIn, account]);
  return status;
}

export function highlightedSnippet(snippet: string): ReactNode {
  return snippet.split(/(⟦[^⟧]*⟧)/g).map((part, index) => part.startsWith('⟦') && part.endsWith('⟧') ? <mark key={index}>{part.slice(1, -1)}</mark> : part);
}

export function LocalMailStatus() {
  const signedIn = useAuthStore(state => state.isAuthenticated);
  const account = useMailStore(state => state.activeAccountId);
  const query = useMailStore(state => state.searchQuery);
  const status = useIndexStatus();
  const [answer, setAnswer] = useState<IndexResponse | null>(null);
  useEffect(() => { setAnswer(null); }, [account, query, signedIn]);
  useEffect(() => {
    const receive = (event: Event) => {
      const answer = (event as CustomEvent<IndexResponse>).detail;
      const state = useMailStore.getState();
      const path = new URL(answer.path, 'https://desktop.invalid');
      const match = path.pathname.match(/^\/api\/v1\/accounts\/(\d+)\/search$/);
      if (match && Number(match[1]) === state.activeAccountId && path.searchParams.get('q') === state.searchQuery && (path.searchParams.get('folder') ?? '') === state.activeFolder) {
        setAnswer(answer);
        if (answer.source === 'local+server' && answer.result?.data) useMailStore.setState({ searchResults: answer.result.data, isSearching: false });
      }
    };
    window.addEventListener(indexResponseEvent, receive);
    return () => window.removeEventListener(indexResponseEvent, receive);
  }, []);
  if (!signedIn || !status) return null;
  const offline = status.offline || answer?.offline;
  const source = answer?.source === 'local+server' ? 'Local index + server' : 'Local index';
  return <aside className="desktop-local-status" aria-label="Local mail index" data-testid="local-mail-status">
    <span role="status">{offline ? 'Offline — synced mail available' : query ? source : 'Local mail'} · {status.bodies.toLocaleString()}/{status.messages.toLocaleString()} messages cached{!status.complete && ' · indexing'}</span>
    {query && !!answer?.result?.data?.length && <details className="desktop-local-snippets"><summary>Ranked matches &amp; highlighted snippets</summary><ol>{answer.result.data.map(hit => <li key={hit.id}><button onClick={() => { void useMailStore.getState().selectMessage(hit.id); }}><strong>{hit.subject || '(no subject)'}</strong><small>{hit.source === 'server' ? 'Server' : 'Local index'}</small><span>{highlightedSnippet(hit.snippet ?? '')}</span></button></li>)}</ol></details>}
  </aside>;
}

export function LocalStorageSettings() {
  const status = useIndexStatus();
  const [notice, setNotice] = useState('');
  if (!status) return null;
  return <section className="desktop-local-settings" aria-label="Local mail storage" data-testid="local-store-settings"><strong>Local mail storage</strong><p>{formatBytes(status.bytes)} on this device</p><p>{status.bodies.toLocaleString()} of {status.messages.toLocaleString()} messages available offline.</p><button onClick={() => { void localMail.reindex(status.account_id).then(() => setNotice('Reindex started. Cached mail stays available.')).catch(() => setNotice('Could not restart indexing.')); }}>Reindex mail</button><small role="status">{notice}</small></section>;
}
