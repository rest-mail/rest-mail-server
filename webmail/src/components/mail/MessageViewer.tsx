import { useState, useEffect } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import * as api from '@/api/client';
import { listAttachments, getAttachmentUrl } from '@/api/client';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { Mail, Paperclip, MessageSquare, Reply, Forward, Trash2, Archive, MoreHorizontal, Eye, EyeOff, Flag, FlagOff, Image, FileText as FileTextIcon, File } from 'lucide-react';
import { toast } from 'sonner';
import DOMPurify from 'dompurify';
import type { Attachment, MessageSummary } from '@/types';
import { CalendarInvite } from './CalendarInvite';

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function attachmentIcon(contentType: string) {
  if (contentType.startsWith('image/')) return <Image className="w-3.5 h-3.5" />;
  if (contentType === 'application/pdf') return <FileTextIcon className="w-3.5 h-3.5" />;
  return <File className="w-3.5 h-3.5" />;
}

/** Compact icon-only action button matching the design (28x28 rounded) */
function ActionBtn({ onClick, title, children }: { onClick: () => void; title: string; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      title={title}
      className="w-7 h-7 rounded-2xl bg-background flex items-center justify-center text-muted-foreground hover:text-foreground transition-colors shrink-0"
    >
      {children}
    </button>
  );
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
      <div className="p-5 space-y-4">
        <div className="h-5 w-64 rounded shimmer" />
        <div className="space-y-2">
          <div className="h-3 w-48 rounded shimmer" />
          <div className="h-3 w-36 rounded shimmer" />
        </div>
        <div className="h-px bg-border my-4" />
        <div className="space-y-2">
          <div className="h-3 w-full rounded shimmer" />
          <div className="h-3 w-5/6 rounded shimmer" />
          <div className="h-3 w-4/6 rounded shimmer" />
        </div>
      </div>
    );
  }

  if (!selectedMessage) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <Mail className="w-10 h-10 stroke-1" />
        <p className="font-mono text-sm">// select_a_message</p>
      </div>
    );
  }

  const msg = selectedMessage;

  if (msg.is_draft) {
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <Mail className="w-10 h-10 stroke-1" />
        <p className="font-mono text-sm">// draft_message</p>
        <button
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
          className="font-mono text-xs text-primary hover:underline"
        >
          [continue_editing]
        </button>
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

    const replyTo = msg.sender;
    let replyCc = '';

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
    <div className="h-full flex flex-col bg-background">
      {/* Action bar — compact icon buttons */}
      <div className="flex items-center gap-1.5 h-10 px-3 bg-secondary shrink-0">
        <ActionBtn onClick={handleReply} title="Reply">
          <Reply className="w-3.5 h-3.5" />
        </ActionBtn>
        <ActionBtn onClick={handleForward} title="Forward">
          <Forward className="w-3.5 h-3.5" />
        </ActionBtn>
        <ActionBtn onClick={() => handleMove('Archive')} title="Archive">
          <Archive className="w-3.5 h-3.5" />
        </ActionBtn>
        <ActionBtn onClick={() => deleteMsg(msg.id)} title="Delete">
          <Trash2 className="w-3.5 h-3.5" />
        </ActionBtn>

        {/* Spacer */}
        <div className="flex-1" />

        {/* More actions dropdown */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="w-7 h-7 rounded-2xl bg-background flex items-center justify-center text-muted-foreground hover:text-foreground transition-colors">
              <MoreHorizontal className="w-3.5 h-3.5" />
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={handleReplyAll}>
              reply_all
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => markRead(msg.id, !msg.is_read)}>
              {msg.is_read ? 'mark_unread' : 'mark_read'}
            </DropdownMenuItem>
            <DropdownMenuItem onClick={() => markFlagged(msg.id, !msg.is_flagged)}>
              {msg.is_flagged ? 'unflag' : 'flag'}
            </DropdownMenuItem>
            {['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk']
              .filter(f => f !== activeFolder)
              .map(f => (
                <DropdownMenuItem key={f} onClick={() => handleMove(f)}>
                  move_to: {f.toLowerCase()}
                </DropdownMenuItem>
              ))}
            {folders
              .filter(f => !['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk'].includes(f.name) && f.name !== activeFolder)
              .map(f => (
                <DropdownMenuItem key={f.name} onClick={() => handleMove(f.name)}>
                  move_to: {f.name.toLowerCase()}
                </DropdownMenuItem>
              ))}
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* Message content */}
      <ScrollArea className="flex-1">
        <div className="px-5 py-4">
          {/* Subject — Oswald uppercase */}
          <h2 className="font-heading-oswald text-xl font-bold mb-3 text-foreground">
            {msg.subject || '(no subject)'}
          </h2>

          {/* Sender row */}
          <div className="flex items-start gap-3">
            <div className="w-8 h-8 rounded-2xl bg-primary flex items-center justify-center font-mono text-xs font-bold text-primary-foreground shrink-0">
              {(msg.sender_name || msg.sender).slice(0, 1).toUpperCase()}
            </div>
            <div className="font-mono text-xs space-y-0.5 text-muted-foreground flex-1 min-w-0">
              <p>
                <span className="font-medium text-foreground">from:</span>{' '}
                {msg.sender_name ? `${msg.sender_name} <${msg.sender}>` : msg.sender}
              </p>
              <p>
                <span className="font-medium text-foreground">date:</span>{' '}
                {formatDate(msg.received_at)}
              </p>
              {msg.recipients_to && (
                <p>
                  <span className="font-medium text-foreground">to:</span>{' '}
                  {typeof msg.recipients_to === 'string' ? msg.recipients_to : JSON.stringify(msg.recipients_to)}
                </p>
              )}
            </div>
          </div>

          {/* Thread indicator */}
          {msg.thread_id && msg.thread_id.length > 0 && (
            <div className="mt-2">
              <button
                className="font-mono text-xs text-muted-foreground hover:text-foreground flex items-center gap-1"
                onClick={handleToggleThread}
                disabled={loadingThread}
              >
                <MessageSquare className="w-3.5 h-3.5" />
                {loadingThread ? '// loading...' : showThread ? '// hide_conversation' : '// show_conversation'}
              </button>
              {showThread && threadMessages.length > 0 && (
                <div className="mt-2 space-y-1">
                  {threadMessages.map(tm => (
                    <button
                      key={tm.id}
                      className="block w-full text-left font-mono text-xs py-2 px-3 rounded-2xl border bg-card hover:bg-accent transition-colors"
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
                <p className="font-mono text-xs text-muted-foreground mt-1 ml-1">// no_other_messages</p>
              )}
            </div>
          )}

          {/* Divider */}
          <div className="h-px bg-border my-4" />

          {/* Attachments */}
          {msg.has_attachments && (
            <div className="mb-4">
              <div className="flex items-center gap-1.5 font-mono text-xs font-medium text-muted-foreground mb-2">
                <Paperclip className="w-3.5 h-3.5" />
                <span>// attachments</span>
              </div>
              {loadingAttachments ? (
                <div className="flex gap-2">
                  <div className="h-7 w-32 rounded-2xl bg-muted animate-pulse" />
                  <div className="h-7 w-28 rounded-2xl bg-muted animate-pulse" />
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
                      className="inline-flex items-center gap-1.5 px-3 py-1 rounded-2xl font-mono text-xs font-medium bg-secondary hover:bg-secondary/80 text-foreground transition-colors"
                    >
                      {attachmentIcon(att.content_type || '')}
                      {att.filename}
                      <span className="text-muted-foreground">({formatFileSize(att.size_bytes)})</span>
                    </a>
                  ))}
                </div>
              )}
              <div className="h-px bg-border mt-4" />
            </div>
          )}

          {/* Calendar Invite */}
          {msg.calendar_events && msg.calendar_events.length > 0 && (
            <CalendarInvite events={msg.calendar_events} messageId={msg.id} />
          )}

          {/* Body */}
          {msg.body_html ? (
            <div
              className="prose prose-sm dark:prose-invert max-w-none font-mono text-[13px] leading-relaxed [&_img]:max-w-full [&_img]:h-auto"
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
            <pre className="font-mono text-[13px] whitespace-pre-wrap text-foreground leading-relaxed">
              {msg.body_text}
            </pre>
          )}
        </div>
      </ScrollArea>
    </div>
  );
}
