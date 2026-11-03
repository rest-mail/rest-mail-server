import { useCallback, useEffect, useState } from "react";
import { dmarcCountsBySource, listDmarcReports } from "../../lib/domainPolicy";
import type { DmarcReport } from "../../lib/domainPolicy";
import { SectionHeading, SectionError } from "./sectionParts";
import { cell, headCell } from "./sectionStyles";

interface DomainDmarcReportsProps {
  domainId: number;
}

const formatDate = (iso: string) =>
  new Date(iso).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
  });

/**
 * The aggregate reports receivers sent about this domain, and per sending IP how
 * many messages passed or failed DMARC: a source that still fails would lose mail
 * under quarantine or reject.
 */
export function DomainDmarcReports({ domainId }: DomainDmarcReportsProps) {
  const [reports, setReports] = useState<DmarcReport[] | null>(null);
  const [total, setTotal] = useState<number | undefined>();
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    setError(null);
    try {
      const result = await listDmarcReports(domainId);
      setReports(result.reports);
      setTotal(result.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not load the DMARC reports");
    }
  }, [domainId]);

  useEffect(() => {
    load();
  }, [load]);

  const sources = reports ? dmarcCountsBySource(reports) : [];
  const failing = sources.filter((s) => s.fail > 0).length;

  return (
    <section aria-labelledby={`dmarc-reports-${domainId}`}>
      <SectionHeading id={`dmarc-reports-${domainId}`}>DMARC reports</SectionHeading>
      {error && <SectionError message={error} onRetry={load} />}
      {!reports && !error && (
        <p className="text-sm" style={{ color: "var(--gray-secondary)" }}>
          Loading DMARC reports...
        </p>
      )}
      {reports && reports.length === 0 && (
        <p className="text-sm" style={{ color: "var(--gray-secondary)" }}>
          No reports yet. Receivers send them daily once the DMARC record is
          published with a rua address.
        </p>
      )}
      {reports && reports.length > 0 && (
        <div className="flex flex-col gap-6">
          <p className="text-sm" style={{ color: "var(--black-soft)" }}>
            {failing === 0
              ? "Every source in these reports passes DMARC."
              : `${failing} ${failing === 1 ? "source fails" : "sources fail"} DMARC. Fix or account for ${failing === 1 ? "it" : "them"} before moving to quarantine or reject.`}
          </p>
          <table className="w-full text-sm">
            <caption className="text-left text-xs mb-2" style={{ color: "var(--gray-secondary)" }}>
              Messages by sending IP
            </caption>
            <thead>
              <tr>
                <th scope="col" className={headCell}>Source IP</th>
                <th scope="col" className={headCell}>Pass</th>
                <th scope="col" className={headCell}>Fail</th>
              </tr>
            </thead>
            <tbody>
              {sources.map((s) => (
                <tr key={s.source}>
                  <td className={cell}>{s.source}</td>
                  <td className={cell} style={{ color: "var(--green-success)" }}>{s.pass}</td>
                  <td className={cell} style={{ color: s.fail ? "#DC2626" : undefined }}>{s.fail}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <table className="w-full text-sm">
            <caption className="text-left text-xs mb-2" style={{ color: "var(--gray-secondary)" }}>
              {total !== undefined && total > reports.length
                ? `${reports.length} of ${total} reports`
                : "Reports"}
            </caption>
            <thead>
              <tr>
                <th scope="col" className={headCell}>Reporter</th>
                <th scope="col" className={headCell}>Period</th>
                <th scope="col" className={headCell}>Policy seen</th>
              </tr>
            </thead>
            <tbody>
              {reports.map((r) => (
                <tr key={r.id}>
                  <td className={cell}>{r.reporting_org || r.reporter}</td>
                  <td className={cell}>
                    {formatDate(r.start_date)} – {formatDate(r.end_date)}
                  </td>
                  <td className={cell}>p={r.policy_published.p}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}
