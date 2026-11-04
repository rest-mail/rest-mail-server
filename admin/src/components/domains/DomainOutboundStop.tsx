import { useState } from "react";
import type { JsonResponse } from "../../lib/api-contract";

type Domain = JsonResponse<"getDomain", 200>["data"];

interface Props {
  domain: Domain;
  // Whether the session may stop and restart it: queue:manage at the server's
  // scope. The API is the real gate; this only hides actions it would refuse.
  canManage: boolean;
  onStop: (reason: string) => Promise<void>;
  onRestart: () => Promise<void>;
}

// DomainOutboundStop shows whether the domain's outbound mail is stopped, and why,
// and lets an operator stop or restart it (#1590). While stopped, the domain's mail
// is accepted and queued but held; mail to it is delivered as before.
export function DomainOutboundStop({ domain, canManage, onStop, onRestart }: Props) {
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const stop = domain.outbound_stop ?? null;

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    try {
      await action();
      setReason("");
    } catch (err) {
      console.error("Outbound stop change failed:", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <h2
        className="text-lg font-semibold mb-4"
        style={{ color: "var(--black-soft)", fontFamily: "Space Grotesk" }}
      >
        Outbound mail
      </h2>
      {stop ? (
        <div className="space-y-2 mb-4" data-testid="outbound-stopped">
          <span
            className="inline-flex items-center h-6 px-2 text-xs font-medium"
            style={{ backgroundColor: "#FEF2F2", color: "#DC2626" }}
          >
            Stopped
          </span>
          <p className="text-sm" style={{ color: "var(--black-soft)" }}>
            {stop.reason}
          </p>
          <p className="text-xs" style={{ color: "var(--gray-secondary)" }}>
            Stopped by {stop.stopped_by || "unknown"} on{" "}
            {new Date(stop.stopped_at).toLocaleString()}. Mail this domain sends
            is queued and held until it is restarted; mail to it is delivered as
            usual.
          </p>
        </div>
      ) : (
        <p className="text-sm mb-4" style={{ color: "var(--gray-secondary)" }}>
          Sending normally.
        </p>
      )}
      {canManage &&
        (stop ? (
          <button
            type="button"
            disabled={busy}
            onClick={() => run(onRestart)}
            className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
            style={{
              borderColor: "var(--gray-border)",
              color: "var(--black-soft)",
              fontFamily: "Space Grotesk",
            }}
          >
            Restart outbound mail
          </button>
        ) : (
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (reason.trim()) run(() => onStop(reason.trim()));
            }}
          >
            <input
              aria-label="Reason for stopping"
              placeholder="Why is this domain's outbound mail being stopped?"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              maxLength={1000}
              className="flex-1 h-10 px-3 text-sm border"
              style={{ borderColor: "var(--gray-border)" }}
            />
            <button
              type="submit"
              disabled={busy || !reason.trim()}
              className="h-10 px-6 flex items-center justify-center text-sm font-medium border"
              style={{
                borderColor: "#EF4444",
                color: "#DC2626",
                fontFamily: "Space Grotesk",
              }}
            >
              Stop outbound mail
            </button>
          </form>
        ))}
    </div>
  );
}
