import type { QuietHoursSetting } from '@/api/client';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { SettingsActions, SettingsField, SettingsSection, SettingsStatus } from '@/components/ui/settings';

export function QuietHoursPreference({ setting, saving, error, onChange, onSave }: {
  setting?: QuietHoursSetting;
  saving: boolean;
  error: string;
  onChange: (setting: QuietHoursSetting) => void;
  onSave: () => void;
}) {
  const disabled = !setting || saving;
  return (
    <SettingsSection title="Notification quiet hours"
      description="Mail still arrives. Push notifications wait until quiet hours end, then you get one summary.">
      <label className="flex items-center gap-2 font-medium">
        <input className="shrink-0" type="checkbox" checked={setting?.enabled ?? false} disabled={disabled}
          onChange={event => setting && onChange({ ...setting, enabled: event.target.checked })} />
        Hold notifications during quiet hours
      </label>
      <div className="flex flex-wrap gap-3">
        <SettingsField label="Start (local time)">
          <Input type="time" value={setting?.start ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, start: event.target.value })} />
        </SettingsField>
        <SettingsField label="End (local time)">
          <Input type="time" value={setting?.end ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, end: event.target.value })} />
        </SettingsField>
        <SettingsField label="IANA time zone">
          <Input type="text" placeholder="Europe/Berlin" value={setting?.time_zone ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, time_zone: event.target.value })} />
        </SettingsField>
      </div>
      <p className="text-xs text-muted-foreground">Start later than end spans midnight. Times follow this zone, including daylight-saving changes.</p>
      <SettingsActions>
        <Button type="button" size="sm" disabled={disabled} onClick={onSave}>
          {saving ? 'Saving...' : 'Save quiet hours'}
        </Button>
      </SettingsActions>
      <SettingsStatus error={error} pending={!setting ? 'Loading quiet hours...' : undefined} />
    </SettingsSection>
  );
}
