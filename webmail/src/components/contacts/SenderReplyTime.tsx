import { useEffect, useState } from 'react';
import { suggestContacts } from '@/api/client';
import { ContactReplyTime } from './ContactReplyTime';

export function SenderReplyTime({ accountId, email }: { accountId: number; email: string }) {
  const [seconds, setSeconds] = useState<number>();
  useEffect(() => {
    let disposed = false;
    suggestContacts(accountId, email).then(response => {
      const contact = response.data.find(row => row.email.toLowerCase() === email.toLowerCase());
      if (!disposed) setSeconds(contact?.usual_reply_seconds);
    }).catch(() => {});
    return () => { disposed = true; };
  }, [accountId, email]);
  return <ContactReplyTime seconds={seconds} />;
}
