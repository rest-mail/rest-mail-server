import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SettingsActions, SettingsField, SettingsSection, SettingsStatus } from '@/components/ui/settings';

export function AccountTimeZonePreference({ saved, draft, saving, error, onDraftChange, onSave, onClear }: {
  /** The stored zone: null when none is set, undefined until it has loaded. */
  saved?: string | null;
  draft: string;
  saving: boolean;
  error: string;
  onDraftChange: (draft: string) => void;
  onSave: () => void;
  onClear: () => void;
}) {
  const disabled = saved === undefined || saving;
  return (
    <SettingsSection title="IMAP search time zone"
      description="Mail apps use this zone to compare dates in IMAP searches. It does not change stored message times.">
      {saved === null && <p className="text-sm">No time zone is set, so IMAP uses UTC.</p>}
      {saved && <p className="text-sm">IMAP uses {saved}.</p>}
      <div className="max-w-64">
        <SettingsField label="IANA time zone">
          <Input type="text" placeholder="Europe/Berlin" value={draft}
            disabled={disabled} onChange={event => onDraftChange(event.target.value)} />
        </SettingsField>
      </div>
      <SettingsActions>
        <Button type="button" size="sm" disabled={disabled} onClick={onSave}>
          {saving ? 'Saving...' : 'Save time zone'}
        </Button>
        <Button type="button" size="sm" variant="outline" disabled={disabled || saved === null} onClick={onClear}>Clear time zone</Button>
      </SettingsActions>
      <SettingsStatus error={error} pending={saved === undefined ? 'Loading the time zone...' : undefined} />
    </SettingsSection>
  );
}
