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
    <section className="rounded-xl border border-border p-6 space-y-3">
      <h3 className="font-semibold">IMAP search time zone</h3>
      <p className="text-sm text-muted-foreground">
        A mail app's date searches over IMAP (SEARCH ON, BEFORE and SINCE) compare calendar days in this zone.
        Stored times do not change.
      </p>
      {saved === null && <p className="text-sm">No time zone is set, so IMAP uses UTC.</p>}
      {saved && <p className="text-sm">IMAP uses {saved}.</p>}
      <div className="text-sm">
        <label>IANA time zone
          <input className="block rounded border border-border bg-background p-2" type="text" placeholder="Europe/Berlin" value={draft}
            disabled={disabled} onChange={event => onDraftChange(event.target.value)} />
        </label>
      </div>
      <div className="flex flex-wrap gap-3">
        <button className="rounded bg-primary px-3 py-2 text-sm text-primary-foreground disabled:opacity-50" disabled={disabled} onClick={onSave}>
          {saving ? 'Saving...' : 'Save time zone'}
        </button>
        <button className="rounded border border-border px-3 py-2 text-sm disabled:opacity-50" disabled={disabled || saved === null} onClick={onClear}>Clear time zone</button>
      </div>
      {saved === undefined && !error && <p role="status">Loading the time zone...</p>}
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
    </section>
  );
}
