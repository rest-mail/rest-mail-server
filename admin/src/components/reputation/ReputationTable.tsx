import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { apiV1 } from "../../lib/api";
import { readJSON } from "../../lib/api-contract";
import type { JsonResponse } from "../../lib/api-contract";

type Reputation = JsonResponse<"getReputation", 200>["data"];
type DomainReputation = Reputation["domains"][number];
type Figures = DomainReputation["current"];

// A window's bounce rate is a spike when it is at least 2% and at least double
// the previous window's (or the previous window sent nothing); its DMARC pass
// rate is a spike when it fell by ten points or more.
const SPIKE_FLOOR = 0.02;
const DMARC_DROP = 0.1;

function bounceSpike(current: Figures, previous: Figures): boolean {
  const now = current.outbound.bounce_rate;
  if (now == null || now < SPIKE_FLOOR) return false;
  const before = previous.outbound.bounce_rate;
  return before == null || now >= 2 * before;
}

function dmarcDrop(current: Figures, previous: Figures): boolean {
  const now = current.dmarc.pass_rate;
  const before = previous.dmarc.pass_rate;
  return now != null && before != null && before - now >= DMARC_DROP;
}

function percent(rate: number | null | undefined): string {
  return rate == null ? "—" : `${(rate * 100).toFixed(1)}%`;
}

interface Props {
  accessToken: string;
}

// ReputationTable is the operator's reputation view (#1590): per domain, its
// bounce, DMARC and complaint figures over the chosen window, worst bounce rate
// first as the API sorts them, spikes against the window before flagged, and a
// link to the domain's outbound stop. The server's DNSBL listings head it.
export function ReputationTable({ accessToken }: Props) {
  const [days, setDays] = useState(7);
  const [report, setReport] = useState<Reputation | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setIsLoading(true);
    setError(null);
    try {
      const response = await apiV1.request(`/admin/reputation?days=${days}`, { method: "GET" });
      const { data } = await readJSON<JsonResponse<"getReputation", 200>>(
        response,
        "Failed to load the reputation view",
      );
      setReport(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load the reputation view");
    } finally {
      setIsLoading(false);
    }
  }, [days]);

  useEffect(() => {
    load();
  }, [load, accessToken]);

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <label className="text-sm" style={{ color: "var(--gray-secondary)" }}>
          Window{" "}
          <select
            aria-label="Window"
            value={days}
            onChange={(e) => setDays(Number(e.target.value))}
            className="h-9 px-2 border text-sm"
            style={{ borderColor: "var(--gray-border)" }}
          >
            <option value={1}>1 day</option>
            <option value={7}>7 days</option>
            <option value={30}>30 days</option>
            <option value={90}>90 days</option>
          </select>
        </label>
        <button
          onClick={load}
          disabled={isLoading}
          className="h-9 px-3 flex items-center gap-2 text-sm border"
          style={{ borderColor: "var(--gray-border)" }}
        >
          <RefreshCw className={`w-4 h-4 ${isLoading ? "animate-spin" : ""}`} />
          Refresh
        </button>
      </div>

      {error && (
        <div className="p-4 border text-sm" style={{ borderColor: "#EF4444", backgroundColor: "#FEF2F2", color: "#DC2626" }}>
          {error}
        </div>
      )}

      {report && (
        <div
          className="p-4 border text-sm"
          data-testid="dnsbl-state"
          style={{
            borderColor: report.dnsbl.listed ? "#EF4444" : "var(--gray-border)",
            backgroundColor: report.dnsbl.listed ? "#FEF2F2" : "white",
          }}
        >
          {report.dnsbl.listed ? (
            <>
              <strong>Sending IPs listed on DNS blocklists</strong> (every domain sends from them):
              <ul className="mt-2 list-disc pl-5">
                {report.dnsbl.listings.map((l) => (
                  <li key={`${l.ip}-${l.provider}-${l.zone}`}>
                    {l.ip} on {l.provider} ({l.zone}) since {new Date(l.since).toLocaleString()}
                    {l.reason ? `: ${l.reason}` : ""}
                  </li>
                ))}
              </ul>
            </>
          ) : (
            <span>No sending IP is listed on a monitored DNS blocklist.</span>
          )}
        </div>
      )}

      {report && (
        <table className="w-full text-sm border" style={{ borderColor: "var(--gray-border)" }}>
          <thead>
            <tr className="text-left" style={{ color: "var(--gray-secondary)" }}>
              <th className="p-3">Domain</th>
              <th className="p-3">Bounce rate</th>
              <th className="p-3">Delivered</th>
              <th className="p-3">Bounced</th>
              <th className="p-3">Deferred</th>
              <th className="p-3">Bounces by reason</th>
              <th className="p-3">DMARC pass</th>
              <th className="p-3">Complaints</th>
              <th className="p-3">Outbound</th>
            </tr>
          </thead>
          <tbody>
            {report.domains.map((d) => {
              const { current, previous } = d;
              const b = current.outbound.bounces;
              const spike = bounceSpike(current, previous);
              const drop = dmarcDrop(current, previous);
              return (
                <tr key={d.domain_id} data-testid={`reputation-${d.domain}`} className="border-t" style={{ borderColor: "var(--gray-border)" }}>
                  <td className="p-3 font-medium">{d.domain}</td>
                  <td className="p-3">
                    <span className="inline-flex items-center gap-1">
                      {percent(current.outbound.bounce_rate)}
                      {spike && (
                        <span
                          className="inline-flex items-center gap-1 h-5 px-1 text-xs font-medium"
                          style={{ backgroundColor: "#FEF2F2", color: "#DC2626" }}
                          title={`Previous window: ${percent(previous.outbound.bounce_rate)}`}
                        >
                          <AlertTriangle className="w-3 h-3" />
                          Spike
                        </span>
                      )}
                    </span>
                  </td>
                  <td className="p-3">{current.outbound.delivered}</td>
                  <td className="p-3">{current.outbound.bounced}</td>
                  <td className="p-3">{current.outbound.deferred}</td>
                  <td className="p-3 text-xs" style={{ color: "var(--gray-secondary)" }}>
                    {current.outbound.bounced === 0
                      ? "—"
                      : [
                          ["no mailbox", b.bad_mailbox],
                          ["no domain", b.bad_domain],
                          ["policy", b.policy],
                          ["other", b.other_permanent],
                          ["expired", b.expired],
                        ]
                          .filter(([, n]) => Number(n) > 0)
                          .map(([label, n]) => `${label} ${n}`)
                          .join(", ")}
                  </td>
                  <td className="p-3">
                    {percent(current.dmarc.pass_rate)}
                    {drop && (
                      <span
                        className="ml-1 h-5 px-1 text-xs font-medium"
                        style={{ backgroundColor: "#FEF2F2", color: "#DC2626" }}
                        title={`Previous window: ${percent(previous.dmarc.pass_rate)}`}
                      >
                        Drop
                      </span>
                    )}
                  </td>
                  <td className="p-3" title="Complaint reports are not recorded yet">
                    {current.complaints ?? "—"}
                  </td>
                  <td className="p-3">
                    <a
                      href={`/domains/${d.domain_id}#outbound-stop`}
                      className="text-sm font-medium"
                      style={{ color: "var(--red-primary)" }}
                    >
                      {d.outbound_stop ? "Stopped: restart…" : "Stop…"}
                    </a>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
    </div>
  );
}
