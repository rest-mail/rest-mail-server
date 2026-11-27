import { useRef, useState } from 'react';
import { ApiError, closeExport, importContactsVCard, listContacts, openContactsExport } from '@/api/client';
import { Button } from '@/components/ui/button';
import { SettingsActions, SettingsSection, SettingsStatus } from '@/components/ui/settings';
import { contactsVCardFile, isVCardFile } from '@/lib/contactsVCard';
import { mailPlatform } from '@/platform';

/** serverOrigin names the server in each card's UID, as the console export does. */
function serverOrigin(): string {
  const platform = mailPlatform();
  const host = platform.desktop ? platform.serverHost?.() : undefined;
  if (host) return host.includes('://') ? host : `https://${host}`;
  return window.location.origin;
}

function saveFile(text: string, name: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/vcard;charset=utf-8' }));
  const link = document.createElement('a');
  link.href = url;
  link.download = name;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function reason(error: unknown, fallback: string): string {
  return error instanceof ApiError && error.message ? `${fallback} ${error.message}` : fallback;
}

/**
 * ContactsFileSettings saves the account's contacts as a vCard file and imports
 * one. The export is opened and closed as an audited export session, so it is
 * recorded the same way as the console's export-data contacts.
 */
export function ContactsFileSettings({ accountId }: { accountId: number }) {
  const input = useRef<HTMLInputElement>(null);
  const [busy, setBusy] = useState<'' | 'export' | 'import'>('');
  const [error, setError] = useState('');
  const [result, setResult] = useState('');
  // The desktop window has no file-save bridge yet, so a download link would do nothing there.
  const canSave = !mailPlatform().desktop;

  async function exportContacts() {
    setBusy('export');
    setError('');
    setResult('');
    let session = '';
    try {
      session = (await openContactsExport(accountId)).data.id;
      const file = await contactsVCardFile((offset, limit) => listContacts(accountId, offset, limit), serverOrigin(), accountId);
      await closeExport(accountId, session, 'completed', file.contacts, file.bytes);
      session = '';
      saveFile(file.text, `contacts-${accountId}.vcf`);
      setResult(file.contacts === 1 ? 'Saved 1 contact.' : `Saved ${file.contacts} contacts.`);
    } catch (problem) {
      if (session) await closeExport(accountId, session, 'aborted', 0, 0).catch(() => undefined);
      setError(reason(problem, 'Could not export contacts.'));
    } finally {
      setBusy('');
    }
  }

  async function importFile(file: File | undefined) {
    if (!file) return;
    setError('');
    setResult('');
    if (!isVCardFile(file)) {
      setError('Choose a vCard file (.vcf).');
      return;
    }
    setBusy('import');
    try {
      const { data } = await importContactsVCard(accountId, file);
      setResult(`Imported ${data.imported}, skipped ${data.skipped} already saved or without an address.`);
    } catch (problem) {
      setError(reason(problem, 'Could not import contacts. Nothing was imported.'));
    } finally {
      setBusy('');
      if (input.current) input.current.value = '';
    }
  }

  return (
    <SettingsSection title="Contacts file"
      description={`${canSave ? "Save this account's contacts as a vCard file, or add contacts from one." : 'Add contacts from a vCard file. Export them from the web app.'} Contacts you already have are left as they are, and imported contacts are not trusted until you trust them.`}>
      <SettingsActions>
        {canSave && <Button type="button" size="sm" disabled={busy !== ''} onClick={exportContacts}>
          {busy === 'export' ? 'Exporting...' : 'Export contacts'}
        </Button>}
        <Button type="button" size="sm" variant="outline" disabled={busy !== ''} onClick={() => input.current?.click()}>
          {busy === 'import' ? 'Importing...' : 'Import contacts'}
        </Button>
        <input ref={input} type="file" accept=".vcf,.vcard,text/vcard,text/x-vcard" className="hidden"
          aria-label="vCard file to import" onChange={event => importFile(event.target.files?.[0])} />
      </SettingsActions>
      {result && <p role="status" className="text-sm text-muted-foreground">{result}</p>}
      <SettingsStatus error={error} />
    </SettingsSection>
  );
}
