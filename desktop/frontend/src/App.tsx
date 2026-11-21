import MailApp from '@webmail/App';
import { TooltipProvider } from '@webmail/components/ui/tooltip';
import { SignIn } from './SignIn';
import type { SessionBinding } from './session';
import { useEffect, useState } from 'react';
import { useAuthStore } from '@webmail/stores/authStore';
import { FirstRunSetup } from './FirstRunSetup';

export default function App({ session }: { session: SessionBinding }) {
  const steps = useAuthStore(state => state.setupRequired);
  const [settingUp, setSettingUp] = useState(false);
  useEffect(() => { if (steps.length) setSettingUp(true); }, [steps]);
  return <TooltipProvider>{settingUp || steps.length ? <FirstRunSetup session={session} onFinished={() => setSettingUp(false)} /> : <MailApp loginPage={<SignIn session={session} />} />}</TooltipProvider>;
}
