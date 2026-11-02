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
    <section className="rounded-xl border border-border p-6 space-y-3">
      <h3 className="font-semibold">Contact reply times</h3>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={enabled ?? false} disabled={enabled === undefined || saving}
          onChange={event => onChange(event.target.checked)} />
        Show usual reply times beside contacts
      </label>
      <p className="text-sm text-muted-foreground">
        Off by default. Estimates use only this account's sent mail and matched replies,
        with at least three replies needed. This is not a signal from the contact.
        Nothing leaves the server; no tracking is added.
      </p>
      {enabled === undefined && !error && <p className="text-sm text-muted-foreground">Loading setting...</p>}
      {saving && <p role="status" className="text-sm text-muted-foreground">Saving...</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </section>
  );
}
