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
import { Paintbrush } from 'lucide-react';
import { mailPlatform } from '@webmail/platform';
import { AppearanceSettings } from './appearance/Appearance';
import { appearance, appearanceSettings, useAppearanceSettings } from './appearance/engine';
import { useReaderPaper } from './appearance/reader';

export default function App({ session, events }: { session: SessionBinding; events?: RuntimeEvents }) {
  useReaderPaper();
  const showAppearance = useAppearanceSettings();
  const email = useAuthStore(state => state.user?.email ?? '');
  const signedIn = useAuthStore(state => state.isAuthenticated);
  const server = mailPlatform().serverHost?.() ?? '';
  const steps = useAuthStore(state => state.setupRequired);
  const [settingUp, setSettingUp] = useState(false);
  useEffect(() => { if (steps.length) setSettingUp(true); }, [steps]);
  useEffect(() => { appearance.restore(server, email); }, [server, email]);
  useEffect(() => {
    const shortcut = (event: KeyboardEvent) => { if ((event.metaKey || event.ctrlKey) && event.key === ',') { event.preventDefault(); appearanceSettings.open(); } };
    window.addEventListener('keydown', shortcut);
    return () => window.removeEventListener('keydown', shortcut);
  }, []);
  const mail = settingUp || steps.length ? <FirstRunSetup session={session} onFinished={() => setSettingUp(false)} /> : <><MailApp showToasts={false} loginPage={<SignIn session={session} />} /><DesktopAccountChoice session={session} /></>;
  return <TooltipProvider>{events ? <DesktopTabs events={events}>{mail}</DesktopTabs> : mail}{(!signedIn || steps.length > 0) && <button className="desktop-appearance-open desktop-signin-appearance" data-testid="appearance-open" onClick={appearanceSettings.open}><Paintbrush size={14} /> Appearance</button>}{showAppearance && <AppearanceSettings />}<ThunderbirdImport /><Toaster richColors position="bottom-right" /></TooltipProvider>;
}
