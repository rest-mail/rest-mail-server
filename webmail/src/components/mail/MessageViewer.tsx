import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { Button } from '@/components/ui/button';
import { Separator } from '@/components/ui/separator';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Mail } from 'lucide-react';
import DOMPurify from 'dompurify';

export function MessageViewer() {
  const { selectedMessage, loadingMessage, markRead, markFlagged, deleteMsg } = useMailStore();
  const { startCompose } = useUIStore();

  if (loadingMessage) {
    return (
      <div className="p-6 space-y-4 animate-pulse">
        <div className="h-5 w-64 rounded bg-muted" />
        <div className="space-y-2">
          <div className="h-3.5 w-48 rounded bg-muted" />
          <div className="h-3.5 w-36 rounded bg-muted" />
        </div>
        <div className="h-px bg-muted my-4" />
        <div className="space-y-2">
          <div className="h-3 w-full rounded bg-muted" />
          <div className="h-3 w-5/6 rounded bg-muted" />
          <div className="h-3 w-4/6 rounded bg-muted" />
        </div>
      </div>
    );
  }

  if (!selectedMessage) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <Mail className="w-10 h-10 stroke-1" />
        <p className="text-sm">Select a message to read</p>
      </div>
    );
  }

  const msg = selectedMessage;

  const handleReply = () => {
    const date = new Date(msg.received_at).toLocaleString();
    const sender = msg.sender_name ? `${msg.sender_name} <${msg.sender}>` : msg.sender;
    const quoteHtml = `<p><br></p><blockquote><p>On ${date}, ${sender} wrote:</p>${msg.body_html || `<p>${msg.body_text}</p>`}</blockquote>`;

    startCompose({
      to: msg.sender,
      cc: '',
      bcc: '',
      subject: msg.subject.startsWith('Re:') ? msg.subject : `Re: ${msg.subject}`,
      inReplyTo: msg.message_id,
      quoteHtml,
    });
  };

  const handleForward = () => {
    const date = new Date(msg.received_at).toLocaleString();
    const fwdHeader = `---------- Forwarded message ----------\nFrom: ${msg.sender}\nDate: ${date}\nSubject: ${msg.subject}\n\n`;
    startCompose({
      to: '',
      cc: '',
      bcc: '',
      subject: msg.subject.startsWith('Fwd:') ? msg.subject : `Fwd: ${msg.subject}`,
      quoteHtml: `<p><br></p><p>${fwdHeader.replace(/\n/g, '<br>')}</p>${msg.body_html || `<p>${msg.body_text}</p>`}`,
    });
  };

  const formatDate = (dateStr: string) => {
    return new Date(dateStr).toLocaleString([], {
      weekday: 'short', year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit',
    });
  };

  return (
    <div className="h-full flex flex-col">
      {/* Action bar */}
      <div className="flex items-center gap-1 px-4 py-2">
        <Button variant="ghost" size="sm" onClick={handleReply}>Reply</Button>
        <Button variant="ghost" size="sm" onClick={handleReply}>Reply All</Button>
        <Button variant="ghost" size="sm" onClick={handleForward}>Forward</Button>
        <Separator orientation="vertical" className="h-5 mx-1" />
        <Button variant="ghost" size="sm" onClick={() => deleteMsg(msg.id)}>Delete</Button>
        <Button variant="ghost" size="sm" onClick={() => markRead(msg.id, !msg.is_read)}>
          {msg.is_read ? 'Mark Unread' : 'Mark Read'}
        </Button>
        <Button variant="ghost" size="sm" onClick={() => markFlagged(msg.id, !msg.is_flagged)}>
          {msg.is_flagged ? 'Unflag' : 'Flag'}
        </Button>
      </div>
      <Separator />

      {/* Message content */}
      <ScrollArea className="flex-1">
        <div className="px-6 py-4">
          {/* Headers */}
          <h2 className="text-lg font-semibold mb-2">{msg.subject || '(no subject)'}</h2>
          <div className="text-sm space-y-0.5 text-muted-foreground">
            <p>
              <span className="font-medium text-foreground">From:</span>{' '}
              {msg.sender_name ? `${msg.sender_name} <${msg.sender}>` : msg.sender}
            </p>
            <p>
              <span className="font-medium text-foreground">Date:</span>{' '}
              {formatDate(msg.received_at)}
            </p>
            {msg.recipients_to && (
              <p>
                <span className="font-medium text-foreground">To:</span>{' '}
                {typeof msg.recipients_to === 'string' ? msg.recipients_to : JSON.stringify(msg.recipients_to)}
              </p>
            )}
          </div>
          <Separator className="my-4" />

          {/* Body */}
          {msg.body_html ? (
            <div
              className="prose prose-sm dark:prose-invert max-w-none"
              dangerouslySetInnerHTML={{
                __html: DOMPurify.sanitize(msg.body_html, {
                  ALLOWED_TAGS: ['p', 'br', 'b', 'i', 'u', 'strong', 'em', 'a', 'ul', 'ol', 'li',
                    'h1', 'h2', 'h3', 'h4', 'blockquote', 'pre', 'code', 'img', 'hr', 'table',
                    'thead', 'tbody', 'tr', 'th', 'td', 'span', 'div'],
                  ALLOWED_ATTR: ['href', 'src', 'alt', 'style', 'class', 'target'],
                }),
              }}
            />
          ) : (
            <pre className="text-sm whitespace-pre-wrap font-mono text-foreground">
              {msg.body_text}
            </pre>
          )}
        </div>
      </ScrollArea>
    </div>
  );
}
