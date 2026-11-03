import { messageClassLabel } from '@/lib/messageClass';
import { cn } from '@/lib/utils';

const CLASS_TITLE: Record<string, string> = {
  newsletter: 'The headers say this came through a mailing list. Anyone can send those headers.',
  receipt: 'One of your mail rules marked this as a receipt.',
  notification: 'The headers say this was sent automatically. Anyone can send those headers.',
};

export interface ClassLabelProps {
  messageClass?: string;
  className?: string;
}

// The class of a message in the mail list. It describes the message's shape, and it is not a
// verdict on who sent it or whether it is safe.
export function ClassLabel({ messageClass, className }: ClassLabelProps) {
  const label = messageClassLabel(messageClass);
  if (!label || !messageClass) return null;
  return (
    <span title={CLASS_TITLE[messageClass]} className={cn('shrink-0 text-[10px] text-muted-foreground', className)}>
      {label}
    </span>
  );
}
