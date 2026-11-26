import MailApp from '@webmail/App';
import { TooltipProvider } from '@webmail/components/ui/tooltip';
import { SignIn } from './SignIn';
import type { SessionBinding, RuntimeEvents } from './session';
import { DesktopTabs } from './DesktopTabs';
import { useEffect, useState } from 'react';
import { useAuthStore } from '@webmail/stores/authStore';
import { FirstRunSetup } from './FirstRunSetup';
import { DesktopAccountChoice } from './IMAPAccounts';
import { Toaster } from 'sonner';
import { ThunderbirdImport } from './ThunderbirdImport';
import { mailPlatform } from '@webmail/platform';
import { AppearanceSettings } from './appearance/Appearance';
import { appearance, appearanceSettings, useAppearanceSettings } from './appearance/engine';
import { useReaderPaper } from './appearance/reader';
import { LocalMailStatus } from './localMail';

export default function App({ session, events }: { session: SessionBinding; events?: RuntimeEvents }) {
  useReaderPaper();
  const showAppearance = useAppearanceSettings();
  const email = useAuthStore(state => state.user?.email ?? '');
  const signedIn = useAuthStore(state => state.isAuthenticated);
  const server = mailPlatform().serverHost?.() ?? '';
  const steps = useAuthStore(state => state.setupRequired);
  const [settingUp, setSettingUp] = useState(false);
  const canOpenAppearance = signedIn && !settingUp && steps.length === 0;
  useEffect(() => { if (steps.length) setSettingUp(true); }, [steps]);
  useEffect(() => { appearance.restore(server, email); }, [server, email]);
  useEffect(() => {
    if (!canOpenAppearance) {
      appearanceSettings.close();
      return;
    }
    const shortcut = (event: KeyboardEvent) => { if ((event.metaKey || event.ctrlKey) && event.key === ',') { event.preventDefault(); appearanceSettings.open(); } };
    window.addEventListener('keydown', shortcut);
    return () => window.removeEventListener('keydown', shortcut);
  }, [canOpenAppearance]);
  const mail = settingUp || steps.length ? <FirstRunSetup session={session} onFinished={() => setSettingUp(false)} /> : <><MailApp showToasts={false} loginPage={<SignIn session={session} />} /><DesktopAccountChoice session={session} /></>;
  return (
    <TooltipProvider>
      <div className="flex h-full min-h-0 flex-col">
        <div className="min-h-0 flex-1">{events ? <DesktopTabs events={events}>{mail}</DesktopTabs> : mail}</div>
        <LocalMailStatus />
      </div>
      {canOpenAppearance && showAppearance && <AppearanceSettings />}
      <ThunderbirdImport />
      <Toaster richColors position="bottom-right" />
    </TooltipProvider>
  );
}
