import MailApp from '@webmail/App';
import { TooltipProvider } from '@webmail/components/ui/tooltip';
import { SignIn } from './SignIn';
import type { SessionBinding } from './session';

export default function App({ session }: { session: SessionBinding }) {
  return <TooltipProvider><MailApp loginPage={<SignIn session={session} />} /></TooltipProvider>;
}
