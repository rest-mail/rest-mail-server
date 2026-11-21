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

export default function App({ session, events }: { session: SessionBinding; events?: RuntimeEvents }) {
  const steps = useAuthStore(state => state.setupRequired);
  const [settingUp, setSettingUp] = useState(false);
  useEffect(() => { if (steps.length) setSettingUp(true); }, [steps]);
  const mail = settingUp || steps.length ? <FirstRunSetup session={session} onFinished={() => setSettingUp(false)} /> : <><MailApp showToasts={false} loginPage={<SignIn session={session} />} /><DesktopAccountChoice session={session} /></>;
  return <TooltipProvider>{events ? <DesktopTabs events={events}>{mail}</DesktopTabs> : mail}<Toaster richColors position="bottom-right" /></TooltipProvider>;
}
