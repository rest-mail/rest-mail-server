import { displayAddress } from '@/lib/addressDisplay';
import { useState, useEffect, useRef, useCallback } from 'react';
import { toast } from 'sonner';
import { useUIStore } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { RichTextEditor } from './RichTextEditor';
import { AutocompleteInput } from './AutocompleteInput';
import * as api from '@/api/client';
import { mailPlatform, type NativeAttachment } from '@/platform';
import { scheduleTime, scheduledSendRequest, sendTimeLabel, toLocalInput } from '@/lib/scheduledSend';
import { X, Code, Type, Trash2, Save, Send, CalendarPlus, CalendarX, Clock } from 'lucide-react';

function parseRecipients(raw: string): string[] {
  return raw ? raw.split(',').map(s => s.trim()).filter(Boolean) : [];
}

export interface ComposeLifecycle {
  heldAttachments?: { id: string; filename: string; size: number }[];
  onRemoveAttachment?: (id: string) => void;
  onPickAttachments?: () => void;
  initialState?: import('@/stores/uiStore').ComposeState;
  initialFrom?: string;
  onClose?: () => void;
  onSent?: () => void;
  onDiscarded?: () => void;
  onStateChange?: (state: { dirty: boolean; canSave: boolean; busy: boolean; subject: string }) => void;
  onSaveHandler?: (save: () => Promise<boolean>) => void;
}

export function ComposeView({ heldAttachments, onRemoveAttachment, onPickAttachments, initialState, initialFrom, onClose, onSent, onDiscarded, onStateChange, onSaveHandler }: ComposeLifecycle = {}) {
  const { composeState: paneState, closeCompose: closePane, setView } = useUIStore();
  const composeState = initialState ?? paneState;
  const closeCompose = onClose ?? closePane;
  const { accounts } = useMailStore();

  const [from, setFrom] = useState(initialFrom ?? accounts[0]?.address ?? '');
  const [to, setTo] = useState<string[]>(parseRecipients(composeState?.to || ''));
  const [cc, setCc] = useState<string[]>(parseRecipients(composeState?.cc || ''));
  const [bcc, setBcc] = useState<string[]>(parseRecipients(composeState?.bcc || ''));
  const [subject, setSubject] = useState(composeState?.subject || '');
  const [htmlContent, setHtmlContent] = useState(composeState?.quoteHtml || composeState?.bodyHtml || '');
  const [isHtml, setIsHtml] = useState(true);
  const [plainText, setPlainText] = useState(composeState?.bodyText || '');
  const [sending, setSending] = useState(false);
  const [attachments, setAttachments] = useState<NativeAttachment[]>([]);
  const attachmentsBlocked = attachments.length > 0 || !!heldAttachments?.length;
  const draftBccBlocked = !!mailPlatform().desktop && bcc.length > 0;
  const [draftId, setDraftId] = useState<number | null>(composeState?.draftId || null);
  const [savingDraft, setSavingDraft] = useState(false);
  const [showCalendar, setShowCalendar] = useState(false);
  const [calSummary, setCalSummary] = useState('');
  const [calLocation, setCalLocation] = useState('');
  const [calStart, setCalStart] = useState('');
  const [calEnd, setCalEnd] = useState('');
  const [calAllDay, setCalAllDay] = useState(false);
  const [showSchedule, setShowSchedule] = useState(false);
  const [scheduleAt, setScheduleAt] = useState('');
  const [scheduleError, setScheduleError] = useState('');
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;

  // Auto-save timer
  const autoSaveRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastSaveRef = useRef<string>('');
  const nativeSavedRef = useRef<string | null>(null);

  const getFormSnapshot = useCallback(() => {
    return JSON.stringify({ from, to, cc, bcc, subject, htmlContent, plainText, isHtml });
  }, [from, to, cc, bcc, subject, htmlContent, plainText, isHtml]);

  useEffect(() => {
    if (nativeSavedRef.current === null && composeState?.draftId) nativeSavedRef.current = getFormSnapshot();
    const hasContent = to.length > 0 || cc.length > 0 || bcc.length > 0 || !!subject || !!stripHtml(htmlContent).trim() || !!plainText || attachmentsBlocked;
    onStateChange?.({
      dirty: attachmentsBlocked || (nativeSavedRef.current === null ? hasContent : getFormSnapshot() !== nativeSavedRef.current),
      canSave: !attachmentsBlocked && !composeState?.forwardMessageId && !draftBccBlocked,
      busy: sending || savingDraft,
      subject,
    });
  }, [getFormSnapshot, attachments, attachmentsBlocked, composeState?.draftId, composeState?.forwardMessageId, draftBccBlocked, sending, savingDraft, onStateChange]);

  // Auto-save every 30 seconds if content changed
  useEffect(() => {
    autoSaveRef.current = setInterval(() => {
      const snapshot = getFormSnapshot();
      if (snapshot !== lastSaveRef.current && (to.length > 0 || subject || htmlContent || plainText)) {
        handleSaveDraft(true);
      }
    }, 30000);
    return () => {
      if (autoSaveRef.current) clearInterval(autoSaveRef.current);
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [from, to, cc, bcc, subject, htmlContent, plainText, draftId, isHtml, attachmentsBlocked, composeState?.forwardMessageId]);

  const getDraftData = () => ({
    from,
    to: to.length > 0 ? to : undefined,
    cc: cc.length > 0 ? cc : undefined,
    subject: subject || undefined,
    body_text: isHtml ? stripHtml(htmlContent) : plainText,
    body_html: isHtml ? htmlContent : undefined,
    ...(mailPlatform().desktop && composeState?.inReplyTo ? { in_reply_to: composeState.inReplyTo } : {}),
  });

  const handleSaveDraft = async (silent = false) => {
    if (attachmentsBlocked || composeState?.forwardMessageId || draftBccBlocked) {
      if (!silent) toast.error("Attachments can't be sent or saved yet (#2212). Remove the selected files first.");
      return false;
    }
    setSavingDraft(true);
    try {
      if (draftId) {
        await api.updateDraft(draftId, getDraftData());
      } else {
        const resp = await api.createDraft(getDraftData());
        setDraftId(resp.data.id);
      }
      lastSaveRef.current = getFormSnapshot();
      nativeSavedRef.current = getFormSnapshot();
      if (!silent) toast.success('Draft saved');
      return true;
    } catch (err) {
      if (!silent) toast.error(err instanceof Error ? err.message : 'Failed to save draft');
      return false;
    } finally {
      setSavingDraft(false);
    }
  };

  useEffect(() => { onSaveHandler?.(() => handleSaveDraft(false)); });

  const messageData = (): Parameters<typeof api.sendMessage>[0] => {
    const sendData: Parameters<typeof api.sendMessage>[0] = {
      from,
      to,
      cc: cc.length > 0 ? cc : undefined,
      bcc: bcc.length > 0 ? bcc : undefined,
      subject,
      body_text: isHtml ? stripHtml(htmlContent) : plainText,
      body_html: isHtml ? htmlContent : undefined,
      in_reply_to: composeState?.inReplyTo,
    };
    if (showCalendar && calSummary && calStart && calEnd) {
      sendData.calendar_event = {
        method: 'REQUEST',
        summary: calSummary,
        location: calLocation || undefined,
        dtstart: new Date(calStart).toISOString(),
        dtend: new Date(calEnd).toISOString(),
        all_day: calAllDay,
        attendees: to.map(addr => ({ address: addr, rsvp: true })),
      };
    }
    return sendData;
  };

  // handleSchedule asks the API to send later. The time is the person's own
  // wall time; the request carries it as UTC, and the server judges it (at
  // least a minute ahead, within a year), so its refusal is shown as it is.
  const handleSchedule = async () => {
    if (attachmentsBlocked || composeState?.forwardMessageId) return;
    if (to.length === 0) return;
    setScheduleError('');
    let when;
    try {
      when = scheduleTime(scheduleAt);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : 'Choose a date and time to send');
      return;
    }
    setSending(true);
    try {
      const resp = await api.scheduleMessage(scheduledSendRequest(messageData(), when));
      if (draftId) {
        // The scheduled send keeps its own copy; the draft would be a duplicate.
        try {
          await api.deleteMessage(draftId);
        } catch {
          // ignore — draft may already be gone
        }
      }
      const at = sendTimeLabel(resp.data?.scheduled_at || when.utc);
      toast.success(`Scheduled for ${at}`, {
        description: when.adjusted ? `That time does not exist on that day where you are, so it goes at ${at}.` : undefined,
        action: { label: 'View scheduled', onClick: () => setView('scheduled') },
      });
      if (onSent) onSent(); else closeCompose();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to schedule message';
      setScheduleError(message);
      toast.error(`Not scheduled: ${message}`);
    } finally {
      setSending(false);
    }
  };

  const openSchedule = () => {
    if (!showSchedule && !scheduleAt) {
      // Start an hour ahead, on the minute, so the default is never refused.
      const start = new Date(Date.now() + 60 * 60 * 1000);
      start.setSeconds(0, 0);
      setScheduleAt(toLocalInput(start));
    }
    setScheduleError('');
    setShowSchedule(!showSchedule);
  };

  const handleSend = async () => {
    if (attachmentsBlocked) return;
    if (to.length === 0) return;
    setSending(true);
    try {
      if (composeState?.forwardMessageId) {
        await api.forwardMessage(composeState.forwardMessageId, { from, to, body_text: isHtml ? stripHtml(htmlContent) : plainText });
      } else if (draftId && !draftBccBlocked) {
        // Update draft with latest content then send it
        await api.updateDraft(draftId, getDraftData());
        await api.sendDraft(draftId);
      } else {
        await api.sendMessage(messageData());
        if (draftId && draftBccBlocked) await api.deleteMessage(draftId).catch(() => toast.warning('Message sent, but the old draft could not be removed.'));
      }
      toast.success('Message sent');
      if (onSent) onSent();
      else closeCompose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to send message');
    } finally {
      setSending(false);
    }
  };

  const handleDiscard = async () => {
    if (draftId) {
      try {
        await api.deleteMessage(draftId);
      } catch {
        // ignore — draft may already be gone
      }
    }
    if (onDiscarded) onDiscarded();
    else closeCompose();
  };

  return (
    <div className={mailPlatform().desktop ? 'desktop-compose h-full flex flex-col bg-background' : 'h-full flex flex-col bg-background'}>
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2">
        <h2 className="font-semibold">{draftId ? 'Edit Draft' : 'Compose'}</h2>
        <Button variant="ghost" size="sm" onClick={handleDiscard}>
          <X className="w-4 h-4" />
        </Button>
      </div>
      <Separator />

      {/* Fields */}
      <div className="px-4 py-3 space-y-2">
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">From</Label>
          <select
            data-testid="compose-from"
            value={from}
            onChange={e => setFrom(e.target.value)}
            className="flex-1 h-9 rounded-md border border-input bg-background px-3 text-sm"
          >
            {accounts.map(a => (
              <option key={a.id} value={a.address}>{displayAddress(a.address)}</option>
            ))}
          </select>
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">To</Label>
          <AutocompleteInput testId="compose-to" value={to} onChange={setTo} placeholder="recipient@example.com" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Cc</Label>
          <AutocompleteInput testId="compose-cc" value={cc} onChange={setCc} placeholder="" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Bcc</Label>
          <AutocompleteInput testId="compose-bcc" value={bcc} onChange={setBcc} placeholder="" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Subject</Label>
          <Input data-testid="compose-subject" value={subject} onChange={e => setSubject(e.target.value)} placeholder="Subject" />
        </div>
      </div>
      <Separator />

      {/* Editor toggle + calendar */}
      {(mailPlatform().pickAttachments || heldAttachments) && <div className="px-4 py-2 text-sm space-y-2">
        {!!heldAttachments?.length && <div className="desktop-attachment-chips">{heldAttachments.map(attachment => <div className="desktop-attachment-chip" key={attachment.id} data-testid={`compose-held-${attachment.id}`}>
          <span>{attachment.filename}</span><small>{attachment.size < 1024 ? `${attachment.size} B` : attachment.size < 1048576 ? `${(attachment.size / 1024).toFixed(1)} KB` : `${(attachment.size / 1048576).toFixed(1)} MB`}</small>
          <button aria-label={`Remove ${attachment.filename}`} data-testid={`compose-remove-held-${attachment.id}`} onClick={() => onRemoveAttachment?.(attachment.id)}>×</button>
        </div>)}</div>}
        {attachments.map((attachment, index) => <div key={`${attachment.filename}-${index}`} className="flex gap-3 items-center">
          <span>{attachment.filename}</span><button data-testid={`compose-remove-attachment-${index}`} onClick={() => setAttachments(previous => previous.filter((_, itemIndex) => itemIndex !== index))}>Remove</button>
        </div>)}
        {attachmentsBlocked && <p role="alert">Attachments can't be sent yet — the server update is on its way. Remove them to send or save this draft. <a href="https://github.com/rest-mail/rest-mail-server/issues/2212" target="_blank" rel="noreferrer">Track #2212</a>.</p>}
        {!heldAttachments && !mailPlatform().canSendAttachments && <p className="text-muted-foreground text-xs">Attachments can't be sent yet. <a href="https://github.com/rest-mail/rest-mail-server/issues/2212" target="_blank" rel="noreferrer">Track #2212</a>.</p>}
        {composeState?.forwardMessageId && <p>Original attachments will be preserved when forwarding.</p>}
      </div>}
      <div className="px-4 py-1 flex items-center gap-2">
        <Button
          variant={isHtml ? 'default' : 'ghost'}
          size="sm"
          className="h-7 text-xs"
          onClick={() => setIsHtml(true)}
        >
          <Code className="w-3.5 h-3.5 mr-1" />
          Rich Text
        </Button>
        <Button
          variant={!isHtml ? 'default' : 'ghost'}
          size="sm"
          className="h-7 text-xs"
          onClick={() => setIsHtml(false)}
        >
          <Type className="w-3.5 h-3.5 mr-1" />
          Plain Text
        </Button>
        <div className="flex-1" />
        <Button
          variant={showCalendar ? 'default' : 'ghost'}
          size="sm"
          className="h-7 text-xs"
          onClick={() => setShowCalendar(!showCalendar)}
        >
          {showCalendar ? <CalendarX className="w-3.5 h-3.5 mr-1" /> : <CalendarPlus className="w-3.5 h-3.5 mr-1" />}
          {showCalendar ? 'Remove Invite' : 'Calendar Invite'}
        </Button>
      </div>

      {showCalendar && (
        <div className="px-4 py-2 space-y-2 bg-muted/30 border-y">
          <div className="flex items-center gap-2">
            <Label className="w-16 text-right text-sm text-muted-foreground">Event</Label>
            <Input value={calSummary} onChange={e => setCalSummary(e.target.value)} placeholder="Meeting title" />
          </div>
          <div className="flex items-center gap-2">
            <Label className="w-16 text-right text-sm text-muted-foreground">Location</Label>
            <Input value={calLocation} onChange={e => setCalLocation(e.target.value)} placeholder="Room or URL" />
          </div>
          <div className="flex items-center gap-2">
            <Label className="w-16 text-right text-sm text-muted-foreground">Start</Label>
            <Input
              type={calAllDay ? 'date' : 'datetime-local'}
              value={calStart}
              onChange={e => setCalStart(e.target.value)}
              className="flex-1"
            />
          </div>
          <div className="flex items-center gap-2">
            <Label className="w-16 text-right text-sm text-muted-foreground">End</Label>
            <Input
              type={calAllDay ? 'date' : 'datetime-local'}
              value={calEnd}
              onChange={e => setCalEnd(e.target.value)}
              className="flex-1"
            />
          </div>
          <div className="flex items-center gap-2 pl-[4.5rem]">
            <input
              type="checkbox"
              id="cal-allday"
              checked={calAllDay}
              onChange={e => setCalAllDay(e.target.checked)}
              className="rounded border-input"
            />
            <label htmlFor="cal-allday" className="text-sm text-muted-foreground">All-day event</label>
          </div>
        </div>
      )}

      {/* Editor */}
      <div className="flex-1 overflow-hidden">
        {isHtml ? (
          <RichTextEditor
            content={htmlContent}
            onChange={setHtmlContent}
            placeholder="Write your message..."
          />
        ) : (
          <textarea
            data-testid="compose-body-plain"
            value={plainText}
            onChange={e => setPlainText(e.target.value)}
            className="w-full h-full resize-none p-4 text-sm font-mono bg-background text-foreground outline-none"
            placeholder="Write your message..."
          />
        )}
      </div>

      {showSchedule && (
        <div className="px-4 py-2 space-y-1 bg-muted/30 border-t">
          <div className="flex items-center gap-2">
            <Label htmlFor="compose-send-at" className="text-sm text-muted-foreground">Send at</Label>
            <Input
              id="compose-send-at"
              type="datetime-local"
              value={scheduleAt}
              onChange={e => { setScheduleAt(e.target.value); setScheduleError(''); }}
              className="w-auto"
              aria-invalid={scheduleError !== ''}
              aria-describedby="compose-send-at-hint"
            />
            <Button size="sm" onClick={handleSchedule} disabled={sending || to.length === 0}>
              <Clock className="w-4 h-4 mr-1" />
              {sending ? 'Scheduling...' : 'Schedule'}
            </Button>
          </div>
          <p id="compose-send-at-hint" className="text-xs text-muted-foreground">
            Your time zone: {timeZone}. It must be at least a minute ahead and within a year.
          </p>
          {scheduleError && (
            <p role="alert" className="text-sm text-destructive">Not scheduled: {scheduleError}</p>
          )}
        </div>
      )}

      <Separator />
      {/* Footer */}
      {draftBccBlocked && <p className="px-4 text-xs text-muted-foreground">Bcc recipients are included when sending. The server cannot store them in a draft.</p>}
      <div className="flex items-center justify-end gap-2 px-4 py-2">
        {(mailPlatform().pickAttachments || onPickAttachments) && <Button data-testid="compose-attach" variant="outline" size="sm" disabled={!onPickAttachments && !mailPlatform().canSendAttachments} title="Sending attachments isn't available yet (#2212)" onClick={async () => {
          if (onPickAttachments) { onPickAttachments(); return; }
          try { const picked = await mailPlatform().pickAttachments!(); setAttachments(previous => [...previous, ...picked]); }
          catch (error) { toast.error(error instanceof Error ? error.message : 'Could not select files'); }
        }}>Attach</Button>}
        <Button variant="outline" size="sm" onClick={handleDiscard}>
          <Trash2 className="w-4 h-4 mr-1" />
          Discard
        </Button>
        <Button data-testid="compose-save" variant="outline" size="sm" onClick={() => handleSaveDraft(false)} disabled={savingDraft || attachmentsBlocked || draftBccBlocked || !!composeState?.forwardMessageId}>
          <Save className="w-4 h-4 mr-1" />
          {savingDraft ? 'Saving...' : mailPlatform().desktop ? 'Save draft' : 'Save Draft'}
        </Button>
        <Button variant={showSchedule ? 'default' : 'outline'} size="sm" onClick={openSchedule} disabled={attachmentsBlocked || !!composeState?.forwardMessageId} aria-expanded={showSchedule}>
          <Clock className="w-4 h-4 mr-1" />
          Send later
        </Button>
        <Button data-testid="compose-send" size="sm" onClick={handleSend} disabled={sending || attachmentsBlocked || (composeState?.forwardMessageId !== undefined && (cc.length > 0 || bcc.length > 0))}>
          <Send className="w-4 h-4 mr-1" />
          {sending ? 'Sending...' : 'Send'}
        </Button>
      </div>
    </div>
  );
}

function stripHtml(html: string): string {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  return doc.body.textContent || '';
}
