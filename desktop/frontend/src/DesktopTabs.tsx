import { useCallback, useEffect, useRef, type CSSProperties, type ReactNode } from 'react';
import { Columns3, PanelsTopLeft, PanelLeft, PanelBottom, ArrowLeft, Inbox, Paintbrush, Plus } from 'lucide-react';
import { ComposeView, type ComposeLifecycle } from '@webmail/components/compose/ComposeView';
import { useAuthStore } from '@webmail/stores/authStore';
import { useMailStore } from '@webmail/stores/mailStore';
import { useUIStore } from '@webmail/stores/uiStore';
import { mailPlatform } from '@webmail/platform';
import { layoutPreferences, useLayoutPreferences } from './layout';
import { tabs, useDesktopTabs, type ComposeTab } from './tabs';
import { native } from './native';
import type { RuntimeEvents } from './session';
import { appearanceSettings } from './appearance/engine';

function Draft({ tab, events }: { tab: ComposeTab; events: RuntimeEvents }) {
  const save = useRef<() => Promise<boolean>>(async () => false);
  const update = useCallback<NonNullable<ComposeLifecycle['onStateChange']>>(status => { tabs.update(tab.id, status); native.syncCompose(tab.id, status); }, [tab.id]);
  const register = useCallback<NonNullable<ComposeLifecycle['onSaveHandler']>>(handler => { save.current = handler; }, []);
  useEffect(() => events.on('native:save-close', async id => {
    if (id !== tab.id) return;
    if (await save.current()) native.completeCompose(tab.id);
    else native.cancelClose(tab.id);
  }), [events, tab.id]);
  return <ComposeView initialState={tab.initialState} initialFrom={tab.from} onStateChange={update} onSaveHandler={register}
    heldAttachments={tab.attachments} onRemoveAttachment={id => native.removeAttachment(tab.id, id)} onPickAttachments={native.pickAttachments}
    onClose={() => native.requestClose(tab.id)} onSent={() => native.completeCompose(tab.id, true)} onDiscarded={() => native.completeCompose(tab.id)} />;
}

export function DesktopTabs({ children, events }: { children: ReactNode; events: RuntimeEvents }) {
  const state = useDesktopTabs();
  const preferences = useLayoutPreferences();
  const email = useAuthStore(auth => auth.user?.email ?? '');
  const signedIn = useAuthStore(auth => auth.isAuthenticated && !auth.setupRequired.length);
  const selectedMessageId = useMailStore(mail => mail.selectedMessageId);
  const view = useUIStore(ui => ui.view);
  const container = useRef<HTMLDivElement>(null);
  const reading = signedIn && !preferences.reader && selectedMessageId !== null && view === 'mail';
  useEffect(() => { layoutPreferences.restore(mailPlatform().serverHost?.() ?? '', signedIn ? email : ''); }, [signedIn, email]);
  useEffect(() => { if (!signedIn) tabs.clear(); }, [signedIn]);
  useEffect(() => {
    const pane = container.current?.querySelector<HTMLElement>('.desktop-list-pane');
    pane?.style.removeProperty('width');
    pane?.style.removeProperty('height');
    if (!pane || !preferences.reader || reading || view !== 'mail' || state.active !== 'mail' || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => {
      const size = pane.getBoundingClientRect();
      layoutPreferences.resize(preferences.layout === 'columns' ? size.width : size.height);
    });
    observer.observe(pane);
    return () => observer.disconnect();
  }, [signedIn, preferences.layout, preferences.reader, reading, view, state.active]);
  const sizes = { '--desktop-list-width': `${preferences.columnsWidth}px`, '--desktop-list-height': `${preferences.stackedHeight}px` } as CSSProperties;
  return <div ref={container} style={sizes} data-layout={preferences.layout} data-folders={preferences.folders} data-reader={preferences.reader} data-reading={reading} data-file-drop-target={signedIn ? '' : undefined} className={`desktop-tabs ${signedIn ? 'desktop-tabs-ready' : ''}`}>
    {signedIn && <header className="desktop-titlebar">
      <div className="desktop-brand" aria-label="REST Mail"><img src="/rest-mail-icon.svg" alt="" width="32" height="32" /><span>REST Mail</span></div>
      <div className="desktop-tab-strip" role="tablist" aria-label="Mail and drafts">
      <button role="tab" data-testid="tab-mail" aria-selected={state.active === 'mail'} aria-controls="panel-mail" onClick={() => tabs.select('mail')}><Inbox size={16} aria-hidden="true" />Mail</button>
      {state.tabs.map(tab => <div className="desktop-draft-tab" key={tab.id} data-active={state.active === tab.id}>
        <button role="tab" data-testid={`tab-${tab.id}`} aria-selected={state.active === tab.id} aria-controls={`panel-${tab.id}`} onClick={() => tabs.select(tab.id)}><span>{tab.dirty ? '• ' : ''}{tab.title}</span></button>
        <button data-testid={`tab-close-${tab.id}`} aria-label={`Close ${tab.title}`} title="Close draft (⌘W)" onClick={() => native.requestClose(tab.id)}>×</button>
      </div>)}
      <button data-testid="tab-new" aria-label="New message" title="New Message (⌘N)" onClick={() => native.openCompose()}><Plus size={16} aria-hidden="true" /></button>
      </div>
      <div className="desktop-layout-controls" role="group" aria-label="Mail layout">
        <div className="desktop-layout-segment" role="group" aria-label="Pane arrangement">
          <button data-testid="layout-columns" aria-label="Three columns" title="Three columns" aria-pressed={preferences.layout === 'columns'} onClick={() => layoutPreferences.select('columns')}><Columns3 size={16} /></button>
          <button data-testid="layout-stacked" aria-label="List above reader" title="List above reader" aria-pressed={preferences.layout === 'stacked'} onClick={() => layoutPreferences.select('stacked')}><PanelsTopLeft size={16} /></button>
        </div>
        <span className="desktop-layout-divider" />
        <button data-testid="pane-folders" aria-label="Show folder pane" title="Show/hide folder pane (⌘⌥S)" aria-pressed={preferences.folders} onClick={layoutPreferences.toggleFolders}><PanelLeft size={16} /></button>
        <button data-testid="pane-reader" aria-label="Show reader pane" title="Show/hide reader pane (⌘⌥R)" aria-pressed={preferences.reader} onClick={layoutPreferences.toggleReader}><PanelBottom size={16} /></button>
        <button data-testid="appearance-open" aria-label="Appearance settings" title="Settings (⌘,)" onClick={appearanceSettings.open}><Paintbrush size={16} /></button>
      </div>
    </header>}
    {reading && state.active === 'mail' && <div className="desktop-reader-back"><button data-testid="reader-back" onClick={() => useMailStore.setState({ selectedMessageId: null, selectedMessage: null })}><ArrowLeft size={16} />Back to messages</button></div>}
    <div className="desktop-tab-panel" id="panel-mail" role="tabpanel" hidden={signedIn && state.active !== 'mail'}>{children}</div>
    {signedIn && state.tabs.map(tab => <div className="desktop-tab-panel" id={`panel-${tab.id}`} role="tabpanel" hidden={state.active !== tab.id} key={tab.id}><Draft tab={tab} events={events} /></div>)}
  </div>;
}
