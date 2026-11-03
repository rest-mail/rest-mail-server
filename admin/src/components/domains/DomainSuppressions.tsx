import { useCallback, useEffect, useId, useState } from "react";
import { clearSuppression, listSuppressions } from "../../lib/domainPolicy";
import type { Suppression } from "../../lib/domainPolicy";
import { SectionError, SectionHeading } from "./sectionParts";
import { cell, headCell, labelStyle } from "./sectionStyles";

interface DomainSuppressionsProps {
  domainId: number;
  /** Clearing needs domains:write; without it the list is read-only. */
  canClear: boolean;
}

const formatDate = (iso: string) => new Date(iso).toLocaleDateString();

/**
 * Addresses this domain stopped sending to after a hard bounce. Clearing one lets
 * mail to it go out again before the suppression expires.
 */
export function DomainSuppressions({ domainId, canClear }: DomainSuppressionsProps) {
  const [items, setItems] = useState<Suppression[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<Suppression | null>(null);
  const [busy, setBusy] = useState(false);
  const titleId = useId();

  const load = useCallback(async () => {
    setError(null);
    try {
      setItems(await listSuppressions(domainId));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load the suppressed addresses");
    }
  }, [domainId]);

  useEffect(() => {
    load();
  }, [load]);

  const confirmClear = async () => {
    if (!confirming) return;
    setBusy(true);
    setError(null);
    try {
      await clearSuppression(domainId, confirming.id);
      setItems((current) => current?.filter((s) => s.id !== confirming.id) ?? null);
      setConfirming(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not clear the suppression");
      setConfirming(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section aria-labelledby={`suppressions-${domainId}`}>
      <SectionHeading id={`suppressions-${domainId}`}>Suppressed addresses</SectionHeading>
      <p className="text-sm mb-4" style={labelStyle}>
        Mail to these addresses is not sent after a hard bounce, until the
        suppression expires. Clear one if the bounce was a mistake.
      </p>
      {error && <SectionError message={error} onRetry={items ? undefined : load} />}
      {!items && !error && (
        <p className="text-sm" style={labelStyle}>
          Loading suppressed addresses...
        </p>
      )}
      {items && items.length === 0 && (
        <p className="text-sm" style={labelStyle}>
          No addresses are suppressed.
        </p>
      )}
      {items && items.length > 0 && (
        <table className="w-full text-sm">
          <thead>
            <tr>
              <th scope="col" className={headCell}>Address</th>
              <th scope="col" className={headCell}>Last error</th>
              <th scope="col" className={headCell}>Expires</th>
              {canClear && (
                <th scope="col" className={headCell}>
                  <span className="sr-only">Actions</span>
                </th>
              )}
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id}>
                <td className={cell}>{s.address}</td>
                <td className={cell} style={labelStyle}>{s.last_error}</td>
                <td className={cell}>{formatDate(s.expires_at)}</td>
                {canClear && (
                  <td className={cell}>
                    <button
                      type="button"
                      onClick={() => setConfirming(s)}
                      aria-label={`Clear ${s.address}`}
                      className="text-sm font-medium"
                      style={{ color: "#DC2626" }}
                    >
                      Clear
                    </button>
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {confirming && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
          <div
            role="alertdialog"
            aria-modal="true"
            aria-labelledby={titleId}
            className="bg-white p-6 w-full max-w-md"
          >
            <h3
              id={titleId}
              className="text-lg font-semibold mb-2"
              style={{ fontFamily: "Space Grotesk", color: "var(--black-soft)" }}
            >
              Clear {confirming.address}?
            </h3>
            <p className="text-sm mb-6" style={labelStyle}>
              Mail to this address will be sent again. If it still bounces, it
              is suppressed again.
            </p>
            <div className="flex justify-end gap-3">
              <button
                type="button"
                onClick={() => setConfirming(null)}
                className="h-10 px-6 text-sm font-medium border"
                style={{ borderColor: "var(--gray-border)", color: "var(--gray-secondary)", fontFamily: "Space Grotesk" }}
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={confirmClear}
                disabled={busy}
                className="h-10 px-6 text-sm font-medium text-white"
                style={{ backgroundColor: "#DC2626", fontFamily: "Space Grotesk", opacity: busy ? 0.6 : 1 }}
              >
                Clear suppression
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
