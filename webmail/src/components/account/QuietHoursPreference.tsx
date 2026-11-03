import type { QuietHoursSetting } from '@/api/client';

export function QuietHoursPreference({ setting, saving, error, onChange, onSave }: {
  setting?: QuietHoursSetting;
  saving: boolean;
  error: string;
  onChange: (setting: QuietHoursSetting) => void;
  onSave: () => void;
}) {
  const disabled = !setting || saving;
  return (
    <section className="rounded-xl border border-border p-6 space-y-3">
      <h3 className="font-semibold">Notification quiet hours</h3>
      <p className="text-sm text-muted-foreground">
        Off by default. Mail still arrives. Push notifications wait until quiet hours end,
        then one summary tells you how many messages arrived.
      </p>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={setting?.enabled ?? false} disabled={disabled}
          onChange={event => setting && onChange({ ...setting, enabled: event.target.checked })} />
        Hold notifications during quiet hours
      </label>
      <div className="flex flex-wrap gap-3 text-sm">
        <label>Start (local time)
          <input className="block rounded border border-border bg-background p-2" type="time" value={setting?.start ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, start: event.target.value })} />
        </label>
        <label>End (local time)
          <input className="block rounded border border-border bg-background p-2" type="time" value={setting?.end ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, end: event.target.value })} />
        </label>
        <label>IANA time zone
          <input className="block rounded border border-border bg-background p-2" type="text" placeholder="Europe/Berlin" value={setting?.time_zone ?? ''}
            required={setting?.enabled} disabled={disabled} onChange={event => setting && onChange({ ...setting, time_zone: event.target.value })} />
        </label>
      </div>
      <p className="text-xs text-muted-foreground">Start later than end spans midnight. Times follow this zone, including daylight-saving changes.</p>
      <button className="rounded bg-primary px-3 py-2 text-sm text-primary-foreground disabled:opacity-50" disabled={disabled} onClick={onSave}>
        {saving ? 'Saving...' : 'Save quiet hours'}
      </button>
      {!setting && !error && <p role="status">Loading quiet hours...</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </section>
  );
}
