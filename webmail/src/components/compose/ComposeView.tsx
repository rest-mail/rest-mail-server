import { useState } from 'react';
import { toast } from 'sonner';
import { useUIStore } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import { RichTextEditor } from './RichTextEditor';
import * as api from '@/api/client';

export function ComposeView() {
  const { composeState, closeCompose } = useUIStore();
  const { accounts } = useMailStore();

  const [from, setFrom] = useState(accounts[0]?.address || '');
  const [to, setTo] = useState(composeState?.to || '');
  const [cc, setCc] = useState(composeState?.cc || '');
  const [bcc, setBcc] = useState(composeState?.bcc || '');
  const [subject, setSubject] = useState(composeState?.subject || '');
  const [htmlContent, setHtmlContent] = useState(composeState?.quoteHtml || '');
  const [isHtml, setIsHtml] = useState(true);
  const [plainText, setPlainText] = useState('');
  const [sending, setSending] = useState(false);

  const handleSend = async () => {
    if (!to.trim()) return;
    setSending(true);
    try {
      const recipients = to.split(',').map(s => s.trim()).filter(Boolean);
      const ccList = cc ? cc.split(',').map(s => s.trim()).filter(Boolean) : undefined;
      const bccList = bcc ? bcc.split(',').map(s => s.trim()).filter(Boolean) : undefined;

      await api.sendMessage({
        from,
        to: recipients,
        cc: ccList,
        bcc: bccList,
        subject,
        body_text: isHtml ? stripHtml(htmlContent) : plainText,
        body_html: isHtml ? htmlContent : undefined,
        in_reply_to: composeState?.inReplyTo,
      });
      toast.success('Message sent');
      closeCompose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to send message');
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2">
        <h2 className="font-semibold">Compose</h2>
        <Button variant="ghost" size="sm" onClick={closeCompose}>
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
          <Input value={to} onChange={e => setTo(e.target.value)} placeholder="recipient@example.com" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Cc</Label>
          <Input value={cc} onChange={e => setCc(e.target.value)} placeholder="" />
        </div>
        <div className="flex items-center gap-2">
          <Label className="w-12 text-right text-sm text-muted-foreground">Bcc</Label>
          <Input value={bcc} onChange={e => setBcc(e.target.value)} placeholder="" />
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
        <Button variant="outline" size="sm" onClick={closeCompose}>
          Discard
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
