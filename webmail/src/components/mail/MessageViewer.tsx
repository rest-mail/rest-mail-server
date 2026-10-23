import { displayAddress } from '@/lib/addressDisplay';
import { useState, useEffect } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { getThemePack, TYPEFACE_CLASS, type ActionStyle } from '@/theme/packs';
import * as api from '@/api/client';
import { listAttachments, getAttachmentUrl } from '@/api/client';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { Mail, Paperclip, MessageSquare, Reply, Forward, Trash2, Archive, MoreHorizontal, Image, FileText as FileTextIcon, File } from 'lucide-react';
import { toast } from 'sonner';
import type { Attachment, MessageSummary } from '@/types';
import { CalendarInvite } from './CalendarInvite';
import { HtmlMessageBody } from './HtmlMessageBody';
import { TrustBadge, TrustReceipt } from './signals';
import { cn } from '@/lib/utils';

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

/** Word-labelled action button: 'keyed' shows a [k] hint, 'words' is plain
 *  small-caps — the two non-icon action styles a theme pack can pin. */
function ActionWord({ k, label, style, onClick }: {
  k: string;
  label: string;
  style: Extract<ActionStyle, 'words' | 'keyed'>;
  onClick: () => void;
}) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "h-7 px-2 flex items-center gap-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-background transition-colors shrink-0",
        style === 'words'
          ? "uppercase tracking-[0.14em] text-[10px] font-semibold"
          : "text-xs font-medium",
      )}
    >
      {style === 'keyed' && (
        <kbd className="px-1 py-0.5 border border-border rounded bg-background text-[9px]">{k}</kbd>
      )}
      {label}
    </button>
  );
}

export function MessageViewer() {
  const { selectedMessage, loadingMessage, markRead, markFlagged, deleteMsg, accounts, refresh, folders, activeFolder, activeAccountId, selectMessage } = useMailStore();
  const { startCompose } = useUIStore();
  const pack = useUIStore(s => getThemePack(s.theme));
  const v = pack.variants;
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
            startCompose({
              to: (msg.recipients_to || []).join(', '),
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

    const activeAccount = accounts.find(a => a.id === activeAccountId);
    const currentUser = activeAccount?.address || accounts[0]?.address || '';

    const allRecipients = (msg.recipients_to || []).filter(addr =>
      addr.toLowerCase() !== currentUser.toLowerCase() &&
      addr.toLowerCase() !== msg.sender.toLowerCase()
    );
    const replyCc = allRecipients.join(', ');

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
      {/* Action bar — style pinned by the active theme pack */}
      <div className="flex items-center gap-1.5 h-10 px-3 bg-secondary shrink-0">
        {v.actions === 'icons' ? (
          <>
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
          </>
        ) : (
          <>
            <ActionWord k="r" label="reply" style={v.actions} onClick={handleReply} />
            <ActionWord k="f" label="forward" style={v.actions} onClick={handleForward} />
            <ActionWord k="e" label="archive" style={v.actions} onClick={() => handleMove('Archive')} />
            <ActionWord k="x" label="delete" style={v.actions} onClick={() => deleteMsg(msg.id)} />
          </>
        )}

        {/* Spacer */}
        <div className="flex-1" />

        {/* More actions dropdown */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            {v.actions === 'icons' ? (
              <button className="w-7 h-7 rounded-2xl bg-background flex items-center justify-center text-muted-foreground hover:text-foreground transition-colors">
                <MoreHorizontal className="w-3.5 h-3.5" />
              </button>
            ) : (
              <button className={cn(
                "h-7 px-2 rounded-md bg-background text-muted-foreground hover:text-foreground transition-colors",
                v.actions === 'words' ? "uppercase tracking-[0.14em] text-[10px] font-semibold" : "text-xs font-medium",
              )}>
                more
              </button>
            )}
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
          {/* Subject — typeface pinned by the pack */}
          <h2 className={cn(
            "mb-3 text-foreground",
            v.readerType === 'serif'
              ? "typeface-serif text-2xl font-medium"
              : v.readerType === 'sans'
                ? "typeface-sans text-xl font-semibold tracking-tight"
                : "font-heading-oswald text-xl font-bold",
          )}>
            {msg.subject || '(no subject)'}
          </h2>

          {/* Sender row */}
          <div className="flex items-start gap-3">
            <div className={cn(
              "w-8 h-8 bg-primary flex items-center justify-center text-xs font-bold text-primary-foreground shrink-0",
              TYPEFACE_CLASS[v.readerType],
              v.identity === 'circle' ? 'rounded-full' : v.identity === 'squircle' ? 'rounded-lg' : 'rounded-2xl',
            )}>
              {(msg.sender_name || displayAddress(msg.sender)).slice(0, 1).toUpperCase()}
            </div>
            <div className={cn("text-xs space-y-0.5 text-muted-foreground flex-1 min-w-0", TYPEFACE_CLASS[v.readerType])}>
              <p className="flex flex-wrap items-center gap-1.5">
                <span className="font-medium text-foreground">from:</span>{' '}
                <span className="min-w-0 break-all">
                  {msg.sender_name ? `${msg.sender_name} <${displayAddress(msg.sender)}>` : displayAddress(msg.sender)}
                </span>
                <TrustBadge headers={msg.headers} compact />
              </p>
              <p>
                <span className="font-medium text-foreground">date:</span>{' '}
                {formatDate(msg.received_at)}
              </p>
              {msg.recipients_to && (
                <p>
                  <span className="font-medium text-foreground">to:</span>{' '}
                  {msg.recipients_to.map(displayAddress).join(', ')}
                </p>
              )}
            </div>
          </div>

          {/* Thread indicator */}
          {msg.thread_id && msg.thread_id.length > 0 && (
            <div className="mt-2">
              <button
                className={cn("text-xs text-muted-foreground hover:text-foreground flex items-center gap-1", TYPEFACE_CLASS[v.readerType])}
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
                      className={cn("block w-full text-left text-xs py-2 px-3 rounded-2xl border bg-card hover:bg-accent transition-colors", TYPEFACE_CLASS[v.readerType])}
                      onClick={() => selectMessage(tm.id)}
                    >
                      <span className="font-medium">{tm.subject || '(no subject)'}</span>
                      <span className="text-muted-foreground ml-2">
                        {new Date(tm.received_at).toLocaleDateString([], { month: 'short', day: 'numeric' })}
                      </span>
                      <span className="text-muted-foreground ml-1">- {tm.sender_name || displayAddress(tm.sender)}</span>
                    </button>
                  ))}
                </div>
              )}
              {showThread && threadMessages.length === 0 && (
                <p className={cn("text-xs text-muted-foreground mt-1 ml-1", TYPEFACE_CLASS[v.readerType])}>// no_other_messages</p>
              )}
            </div>
          )}

          <TrustReceipt headers={msg.headers} receivedAt={msg.received_at} className="mt-3" />

          {/* Divider */}
          <div className="h-px bg-border my-4" />

          {/* Attachments */}
          {msg.has_attachments && (
            <div className="mb-4">
              <div className={cn("flex items-center gap-1.5 text-xs font-medium text-muted-foreground mb-2", TYPEFACE_CLASS[v.readerType])}>
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
                      className={cn("inline-flex items-center gap-1.5 px-3 py-1 rounded-2xl text-xs font-medium bg-secondary hover:bg-secondary/80 text-foreground transition-colors", TYPEFACE_CLASS[v.readerType])}
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

          {/* Body — untrusted HTML rendered in a sandboxed, CSP-locked iframe */}
          {msg.body_html ? (
            <HtmlMessageBody html={msg.body_html} />
          ) : (
            <pre className={cn("text-[13px] whitespace-pre-wrap text-foreground leading-relaxed", TYPEFACE_CLASS[v.readerType])}>
              {msg.body_text}
            </pre>
          )}
        </div>
      </ScrollArea>
    </div>
  );
}
