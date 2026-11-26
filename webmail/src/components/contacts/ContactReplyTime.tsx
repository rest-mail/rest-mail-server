import { SettingsSection, SettingsStatus } from '@/components/ui/settings';

export function ContactReplyTime({ seconds }: { seconds?: number | null }) {
  if (seconds == null || !Number.isFinite(seconds) || seconds <= 0) return null;
  let duration = 'less than a minute';
  if (seconds >= 60) {
    const unit = seconds >= 86400 ? 'day' : seconds >= 3600 ? 'hour' : 'minute';
    const divisor = unit === 'day' ? 86400 : unit === 'hour' ? 3600 : 60;
    const amount = Math.round(seconds / divisor);
    duration = `${amount} ${unit}${amount === 1 ? '' : 's'}`;
  }
  return (
    <span className="text-xs text-muted-foreground" title="Local estimate from at least three replies in this account's own history; not a signal from the contact.">
      Usually replies in about {duration}
    </span>
  );
}

export function ContactReplyTimePreference({ enabled, saving, error, onChange }: {
  enabled?: boolean;
  saving: boolean;
  error: string;
  onChange: (enabled: boolean) => void;
}) {
  return (
    <SettingsSection title="Contact reply times"
      description="Estimates use at least three matched replies from this account. Nothing leaves the server; no tracking is added.">
      <label className="flex items-center gap-2 font-medium">
        <input className="shrink-0" type="checkbox" checked={enabled ?? false} disabled={enabled === undefined || saving}
          onChange={event => onChange(event.target.checked)} />
        Show usual reply times beside contacts
      </label>
      <SettingsStatus error={error} pending={saving ? 'Saving...' : enabled === undefined ? 'Loading setting...' : undefined} />
    </SettingsSection>
  );
}
