import { useState, useEffect } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import * as api from '@/api/client';
import { listAttachments, getAttachmentUrl } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Separator } from '@/components/ui/separator';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { Mail, Paperclip, FolderOpen, MessageSquare } from 'lucide-react';
import { toast } from 'sonner';
import DOMPurify from 'dompurify';
import type { Attachment, MessageSummary } from '@/types';

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function MessageViewer() {
  const { selectedMessage, loadingMessage, markRead, markFlagged, deleteMsg, accounts, refresh, folders, activeFolder, activeAccountId, selectMessage } = useMailStore();
  const { startCompose } = useUIStore();
  const [attachments, setAttachments] = useState<Attachment[]>([]);
  const [loadingAttachments, setLoadingAttachments] = useState(false);
  const [showThread, setShowThread] = useState(false);
  const [threadMessages, setThreadMessages] = useState<MessageSummary[]>([]);
  const [loadingThread, setLoadingThread] = useState(false);

  useEffect(() => {
    if (!selectedMessage?.has_attachments) {
      setAttachments([]);
      return;
    }
    let cancelled = false;
    setLoadingAttachments(true);
    listAttachments(selectedMessage.id)
      .then((res) => {
        if (!cancelled) setAttachments(res.data);
      })
      .catch(() => {
        if (!cancelled) setAttachments([]);
      })
      .finally(() => {
        if (!cancelled) setLoadingAttachments(false);
      });
    return () => { cancelled = true; };
  }, [selectedMessage?.id, selectedMessage?.has_attachments]);

  // Reset thread state when message changes
  useEffect(() => {
    setShowThread(false);
    setThreadMessages([]);
  }, [selectedMessage?.id]);

  const handleMove = async (folder: string) => {
    if (!selectedMessage) return;
    try {
      await api.updateMessage(selectedMessage.id, { folder });
      toast.success(`Moved to ${folder}`);
      await refresh();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to move message');
    }
  };

  const handleToggleThread = async () => {
    if (!selectedMessage || !activeAccountId) return;
    if (showThread) {
      setShowThread(false);
      return;
    }
    setLoadingThread(true);
    try {
      const resp = await api.getThread(activeAccountId, selectedMessage.thread_id);
      setThreadMessages(resp.data.filter(m => m.id !== selectedMessage.id));
      setShowThread(true);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to load thread');
    } finally {
      setLoadingThread(false);
    }
  };

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

  // If it's a draft, open in compose mode
  if (msg.is_draft) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <Mail className="w-10 h-10 stroke-1" />
        <p className="text-sm font-medium">Draft message</p>
        <Button
          size="sm"
          onClick={() => {
            let parsedTo: string[] = [];
            try {
              parsedTo = typeof msg.recipients_to === 'string'
                ? JSON.parse(msg.recipients_to)
                : (msg.recipients_to || []);
            } catch { /* ignore */ }

            startCompose({
              to: parsedTo.join(', '),
              cc: '',
              bcc: '',
              subject: msg.subject || '',
              draftId: msg.id,
              bodyHtml: msg.body_html || '',
              bodyText: msg.body_text || '',
            });
          }}
        >
          Continue editing
        </Button>
      </div>
    );
  }

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

  const handleReplyAll = () => {
    const date = new Date(msg.received_at).toLocaleString();
    const sender = msg.sender_name ? `${msg.sender_name} <${msg.sender}>` : msg.sender;
    const quoteHtml = `<p><br></p><blockquote><p>On ${date}, ${sender} wrote:</p>${msg.body_html || `<p>${msg.body_text}</p>`}</blockquote>`;

    // Get all original To recipients except the current user
    const replyTo = msg.sender;
    let replyCc = '';

    // Use the active account's address (not always accounts[0])
    const activeAccount = accounts.find(a => a.id === activeAccountId);
    const currentUser = activeAccount?.address || accounts[0]?.address || '';

    try {
      const originalTo: string[] = typeof msg.recipients_to === 'string'
        ? JSON.parse(msg.recipients_to)
        : (msg.recipients_to || []);
      const allRecipients = originalTo.filter(addr =>
        addr.toLowerCase() !== currentUser.toLowerCase() &&
        addr.toLowerCase() !== msg.sender.toLowerCase()
      );
      replyCc = allRecipients.join(', ');
    } catch {
      // fallback: no CC
    }

    startCompose({
      to: replyTo,
      cc: replyCc,
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
        <Button variant="ghost" size="sm" onClick={handleReplyAll}>Reply All</Button>
        <Button variant="ghost" size="sm" onClick={handleForward}>Forward</Button>
        <Separator orientation="vertical" className="h-5 mx-1" />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm">
              <FolderOpen className="w-4 h-4 mr-1" />
              Move to
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            {['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk']
              .filter(f => f !== activeFolder)
              .map(f => (
                <DropdownMenuItem key={f} onClick={() => handleMove(f)}>{f}</DropdownMenuItem>
              ))}
            {folders
              .filter(f => !['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk'].includes(f.name) && f.name !== activeFolder)
              .map(f => (
                <DropdownMenuItem key={f.name} onClick={() => handleMove(f.name)}>{f.name}</DropdownMenuItem>
              ))}
          </DropdownMenuContent>
        </DropdownMenu>
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

          {/* Thread indicator */}
          {msg.thread_id && msg.thread_id.length > 0 && (
            <div className="mt-2">
              <Button
                variant="ghost"
                size="sm"
                className="text-xs text-muted-foreground hover:text-foreground px-0"
                onClick={handleToggleThread}
                disabled={loadingThread}
              >
                <MessageSquare className="w-3.5 h-3.5 mr-1" />
                {loadingThread ? 'Loading...' : showThread ? 'Hide conversation' : 'Show conversation'}
              </Button>
              {showThread && threadMessages.length > 0 && (
                <div className="mt-2 ml-1 border-l-2 border-muted pl-3 space-y-1">
                  {threadMessages.map(tm => (
                    <button
                      key={tm.id}
                      className="block w-full text-left text-xs py-1 px-2 rounded hover:bg-muted transition-colors"
                      onClick={() => selectMessage(tm.id)}
                    >
                      <span className="font-medium">{tm.subject || '(no subject)'}</span>
                      <span className="text-muted-foreground ml-2">
                        {new Date(tm.received_at).toLocaleDateString([], { month: 'short', day: 'numeric' })}
                      </span>
                      <span className="text-muted-foreground ml-1">- {tm.sender_name || tm.sender}</span>
                    </button>
                  ))}
                </div>
              )}
              {showThread && threadMessages.length === 0 && (
                <p className="text-xs text-muted-foreground mt-1 ml-1">No other messages in this conversation.</p>
              )}
            </div>
          )}

          <Separator className="my-4" />

          {/* Attachments */}
          {msg.has_attachments && (
            <div className="mb-4">
              <div className="flex items-center gap-1.5 text-sm font-medium text-muted-foreground mb-2">
                <Paperclip className="w-4 h-4" />
                <span>Attachments</span>
              </div>
              {loadingAttachments ? (
                <div className="flex gap-2">
                  <div className="h-7 w-32 rounded-full bg-muted animate-pulse" />
                  <div className="h-7 w-28 rounded-full bg-muted animate-pulse" />
                </div>
              ) : (
                <div className="flex flex-wrap gap-2">
                  {attachments.map((att) => (
                    <a
                      key={att.id}
                      href={getAttachmentUrl(att.id)}
                      target="_blank"
                      rel="noopener noreferrer"
                      download={att.filename}
                      className="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium bg-muted hover:bg-muted/80 text-foreground transition-colors"
                    >
                      <Paperclip className="w-3 h-3" />
                      {att.filename}
                      <span className="text-muted-foreground">({formatFileSize(att.size_bytes)})</span>
                    </a>
                  ))}
                </div>
              )}
              <Separator className="mt-4" />
            </div>
          )}

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
