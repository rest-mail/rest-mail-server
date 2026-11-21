import { useCallback, useEffect, useRef, type ReactNode } from 'react';
import { ComposeView, type ComposeLifecycle } from '@webmail/components/compose/ComposeView';
import { useAuthStore } from '@webmail/stores/authStore';
import { tabs, useDesktopTabs, type ComposeTab } from './tabs';
import { native } from './native';
import type { RuntimeEvents } from './session';

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
  const signedIn = useAuthStore(auth => auth.isAuthenticated && !auth.setupRequired.length);
  useEffect(() => { if (!signedIn) tabs.clear(); }, [signedIn]);
  return <div data-file-drop-target={signedIn ? '' : undefined} className={`desktop-tabs ${signedIn ? 'desktop-tabs-ready' : ''}`}>
    {signedIn && <div className="desktop-tab-strip" role="tablist" aria-label="Mail and drafts">
      <button role="tab" data-testid="tab-mail" aria-selected={state.active === 'mail'} aria-controls="panel-mail" onClick={() => tabs.select('mail')}>Mail</button>
      {state.tabs.map(tab => <div className="desktop-draft-tab" key={tab.id} data-active={state.active === tab.id}>
        <button role="tab" data-testid={`tab-${tab.id}`} aria-selected={state.active === tab.id} aria-controls={`panel-${tab.id}`} onClick={() => tabs.select(tab.id)}><span>{tab.dirty ? '• ' : ''}{tab.title}</span></button>
        <button data-testid={`tab-close-${tab.id}`} aria-label={`Close ${tab.title}`} title="Close draft (⌘W)" onClick={() => native.requestClose(tab.id)}>×</button>
      </div>)}
      <button data-testid="tab-new" aria-label="New message" title="New Message (⌘N)" onClick={() => native.openCompose()}>+</button>
    </div>}
    <div className="desktop-tab-panel" id="panel-mail" role="tabpanel" hidden={signedIn && state.active !== 'mail'}>{children}</div>
    {signedIn && state.tabs.map(tab => <div className="desktop-tab-panel" id={`panel-${tab.id}`} role="tabpanel" hidden={state.active !== tab.id} key={tab.id}><Draft tab={tab} events={events} /></div>)}
  </div>;
}
