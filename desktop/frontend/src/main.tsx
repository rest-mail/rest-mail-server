import ReactDOM from 'react-dom/client';
import { configureMailPlatform } from '@webmail/platform';
import { session, runtimeEvents, nativeFiles, identity } from './session';
import { createDesktopTransport } from './transport';
import { openNativeMailStream } from './mailStream';
import { prepareScreenshotDocument, registerUIBridge } from './bridge';
import './desktop.css';

async function launch() {
  const windowName = 'main';
  const mock = import.meta.env.DESKTOP_MOCK ? await import('../dev/session') : null;
  const binding = mock?.session ?? session;
  const events = mock?.events ?? runtimeEvents;
  const transport = createDesktopTransport(mock?.mockFetch);
  let serverHost = '';
  configureMailPlatform({
    desktop: true, fetch: transport, streamURL: mock?.streamURL,
    importThunderbird: () => window.dispatchEvent(new Event('desktop:import-thunderbird')),
    openStream: mock ? undefined : openNativeMailStream,
    serverHost: () => serverHost,
    restoreSession: async () => {
      const status = await binding.Status();
      serverHost = status.Server ? new URL(status.Server).host : '';
      return identity(status);
    },
    signOut: () => binding.SignOut(),
    pickAttachments: nativeFiles.pick,
    saveAttachment: async (url, filename) => {
      const response = await transport(url);
      if (!response.ok) throw new Error(`Download failed (${response.status})`);
      const bytes = new Uint8Array(await response.arrayBuffer());
      let binary = '';
      for (const byte of bytes) binary += String.fromCharCode(byte);
      await nativeFiles.save(filename, btoa(binary));
    },
  });
  const [{ useAuthStore }, { useUIStore }, { useMailStore }, { default: App }] = await Promise.all([
    import('@webmail/stores/authStore'), import('@webmail/stores/uiStore'), import('@webmail/stores/mailStore'), import('./App'),
  ]);
  const { native, installNative, registerMessageAction, handleNativeMailEvent } = await import('./native');
  const { tabs } = await import('./tabs');
  if (!mock) {
    const { mailPlatform } = await import('@webmail/platform');
    configureMailPlatform({ ...mailPlatform(), mailboxActive: () => document.hasFocus() && tabs.get().active === 'mail', openCompose: native.openCompose, registerMessageAction, nativeNotifications: true, onMailEvent: handleNativeMailEvent });
    installNative(events, windowName);
  }
  events.on('session:changed', data => {
    const status = data as Awaited<ReturnType<typeof binding.Status>>;
    serverHost = status.Server ? new URL(status.Server).host : '';
    const user = identity(status);
    useAuthStore.setState({ user, isAuthenticated: !!user, sessionChecked: true, setupRequired: status.SetupRequired ?? [] });
    if (!user) useMailStore.setState({ accounts: [], folders: [], messages: [], selectedMessage: null, selectedMessageId: null, activeAccountId: null });
  });
  registerUIBridge(events, {
    windowName,
    tabs: tabs.list,
    route: () => !useAuthStore.getState().isAuthenticated ? '/signin' : useAuthStore.getState().setupRequired.length ? `/setup/${useAuthStore.getState().setupRequired[0]}` : tabs.get().active !== 'mail' ? `/compose/${tabs.get().active}` : `/${useUIStore.getState().view}`,
    navigate: async route => {
      if (route === '/signin') { await binding.SignOut(); return; }
      if (!useAuthStore.getState().isAuthenticated) throw new Error('Sign in first');
      if (route !== '/compose') tabs.select('mail');
      if (route === '/compose') useUIStore.getState().startCompose();
      else if (route === '/mail' || route === '/') useUIStore.getState().setView('mail');
      else if (route.startsWith('/folder/')) { useUIStore.getState().setView('mail'); await useMailStore.getState().selectFolder(decodeURIComponent(route.slice(8))); }
      else if (route.startsWith('/message/')) { useUIStore.getState().setView('mail'); await useMailStore.getState().selectMessage(Number(route.slice(9))); }
      else throw new Error('Unknown route');
    },
    screenshot: async () => {
      const { default: html2canvas } = await import('html2canvas-pro');
      const canvas = await html2canvas(document.body, { onclone: prepareScreenshotDocument, useCORS: false, allowTaint: false, ignoreElements: element => element.hasAttribute('data-ui-sensitive') || element instanceof HTMLInputElement && element.type === 'password' });
      return canvas.toDataURL('image/png').split(',')[1];
    },
  });
  const root = ReactDOM.createRoot(document.getElementById('root')!);
  root.render(<App session={binding} events={events} />);
}
void launch();
