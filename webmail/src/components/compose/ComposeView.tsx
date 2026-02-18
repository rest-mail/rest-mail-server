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

function parseRecipients(raw: string): string[] {
  return raw ? raw.split(',').map(s => s.trim()).filter(Boolean) : [];
}

export function ComposeView() {
  const { composeState, closeCompose } = useUIStore();
  const { accounts } = useMailStore();

  const [from, setFrom] = useState(accounts[0]?.address || '');
  const [to, setTo] = useState<string[]>(parseRecipients(composeState?.to || ''));
  const [cc, setCc] = useState<string[]>(parseRecipients(composeState?.cc || ''));
  const [bcc, setBcc] = useState<string[]>(parseRecipients(composeState?.bcc || ''));
  const [subject, setSubject] = useState(composeState?.subject || '');
  const [htmlContent, setHtmlContent] = useState(composeState?.quoteHtml || composeState?.bodyHtml || '');
  const [isHtml, setIsHtml] = useState(true);
  const [plainText, setPlainText] = useState(composeState?.bodyText || '');
  const [sending, setSending] = useState(false);
  const [draftId, setDraftId] = useState<number | null>(composeState?.draftId || null);
  const [savingDraft, setSavingDraft] = useState(false);

  // Auto-save timer
  const autoSaveRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lastSaveRef = useRef<string>('');

  const getFormSnapshot = useCallback(() => {
    return JSON.stringify({ from, to, cc, bcc, subject, htmlContent, plainText });
  }, [from, to, cc, bcc, subject, htmlContent, plainText]);

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
  }, [from, to, cc, bcc, subject, htmlContent, plainText, draftId]);

  const getDraftData = () => ({
    from,
    to: to.length > 0 ? to : undefined,
    cc: cc.length > 0 ? cc : undefined,
    subject: subject || undefined,
    body_text: isHtml ? stripHtml(htmlContent) : plainText,
    body_html: isHtml ? htmlContent : undefined,
  });

  const handleSaveDraft = async (silent = false) => {
    setSavingDraft(true);
    try {
      if (draftId) {
        await api.updateDraft(draftId, getDraftData());
      } else {
        const resp = await api.createDraft(getDraftData());
        setDraftId(resp.data.id);
      }
      lastSaveRef.current = getFormSnapshot();
      if (!silent) toast.success('Draft saved');
    } catch (err) {
      if (!silent) toast.error(err instanceof Error ? err.message : 'Failed to save draft');
    } finally {
      setSavingDraft(false);
    }
  };

  const handleSend = async () => {
    if (to.length === 0) return;
    setSending(true);
    try {
      if (draftId) {
        // Update draft with latest content then send it
        await api.updateDraft(draftId, getDraftData());
        await api.sendDraft(draftId);
      } else {
        await api.sendMessage({
          from,
          to,
          cc: cc.length > 0 ? cc : undefined,
          bcc: bcc.length > 0 ? bcc : undefined,
          subject,
          body_text: isHtml ? stripHtml(htmlContent) : plainText,
          body_html: isHtml ? htmlContent : undefined,
          in_reply_to: composeState?.inReplyTo,
        });
      }
      toast.success('Message sent');
      closeCompose();
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
    closeCompose();
  };

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2">
        <h2 className="font-semibold">{draftId ? 'Edit Draft' : 'Compose'}</h2>
        <Button variant="ghost" size="sm" onClick={handleDiscard}>
          {"\u2715"}
        </Button>
      </div>
      <Separator />

      {/* Fields */}
      <div className="px-4 py-3 space-y-2">
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">From</Label>
          <select
            value={from}
            onChange={e => setFrom(e.target.value)}
            className="flex-1 h-9 rounded-md border border-input bg-background px-3 text-sm"
          >
            {accounts.map(a => (
              <option key={a.id} value={a.address}>{a.address}</option>
            ))}
          </select>
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">To</Label>
          <AutocompleteInput value={to} onChange={setTo} placeholder="recipient@example.com" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Cc</Label>
          <AutocompleteInput value={cc} onChange={setCc} placeholder="" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Bcc</Label>
          <AutocompleteInput value={bcc} onChange={setBcc} placeholder="" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Subject</Label>
          <Input value={subject} onChange={e => setSubject(e.target.value)} placeholder="Subject" />
        </div>
      </div>
      <Separator />

      {/* Editor toggle */}
      <div className="px-4 py-1 flex items-center gap-2">
        <Button
          variant={isHtml ? 'default' : 'ghost'}
          size="sm"
          className="h-7 text-xs"
          onClick={() => setIsHtml(true)}
        >
          HTML
        </Button>
        <Button
          variant={!isHtml ? 'default' : 'ghost'}
          size="sm"
          className="h-7 text-xs"
          onClick={() => setIsHtml(false)}
        >
          Plain Text
        </Button>
      </div>

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
            value={plainText}
            onChange={e => setPlainText(e.target.value)}
            className="w-full h-full resize-none p-4 text-sm font-mono bg-background text-foreground outline-none"
            placeholder="Write your message..."
          />
        )}
      </div>

      <Separator />
      {/* Footer */}
      <div className="flex items-center justify-end gap-2 px-4 py-2">
        <Button variant="outline" size="sm" onClick={handleDiscard}>
          Discard
        </Button>
        <Button variant="outline" size="sm" onClick={() => handleSaveDraft(false)} disabled={savingDraft}>
          {savingDraft ? 'Saving...' : 'Save Draft'}
        </Button>
        <Button size="sm" onClick={handleSend} disabled={sending}>
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
