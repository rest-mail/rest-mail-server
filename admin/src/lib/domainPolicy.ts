import { apiV1 } from "./api";
import { expectSuccess, readJSON } from "./api-contract";
import type { JsonResponse, RequestBody } from "./api-contract";
import type { components } from "./api-types";

export type DmarcPolicy = components["schemas"]["Domain"]["dmarc_policy"];
export type DmarcReport = components["schemas"]["DMARCReport"];
export type Suppression = components["schemas"]["Suppression"];
export type RelayView = components["schemas"]["relayView"];
export type SetRelayBody = RequestBody<"setDomainRelay">;

export const DMARC_POLICIES: DmarcPolicy[] = ["none", "quarantine", "reject"];

/**
 * Whether the session holds a capability at some scope. It only decides what the
 * page shows: the API checks the capability, and the scope, on every request.
 */
export function holdsCapability(
  capabilities: readonly string[] | undefined,
  capability: string,
): boolean {
  return (capabilities ?? []).some((c) => c === "*" || c === capability);
}

export async function listDmarcReports(
  domainId: number,
): Promise<{ reports: DmarcReport[]; total?: number }> {
  const body = await readJSON<JsonResponse<"listDmarcReports", 200>>(
    await apiV1.request(`/admin/dmarc-reports?domain_id=${domainId}&limit=50`),
    "Could not load the DMARC reports",
  );
  return { reports: body.data, total: body.pagination?.total };
}

export async function listSuppressions(domainId: number): Promise<Suppression[]> {
  const body = await readJSON<JsonResponse<"listSuppressions", 200>>(
    await apiV1.request(`/admin/domains/${domainId}/suppressions`),
    "Could not load the suppressed addresses",
  );
  return body.data;
}

export async function clearSuppression(
  domainId: number,
  suppressionId: number,
): Promise<void> {
  await expectSuccess(
    await apiV1.request(
      `/admin/domains/${domainId}/suppressions/${suppressionId}`,
      { method: "DELETE" },
    ),
    "Could not clear the suppression",
  );
}

export async function getRelay(domainId: number): Promise<RelayView> {
  const body = await readJSON<JsonResponse<"getDomainRelay", 200>>(
    await apiV1.request(`/admin/domains/${domainId}/relay`),
    "Could not load the relay settings",
  );
  return body.data;
}

export async function setRelay(
  domainId: number,
  relay: SetRelayBody,
): Promise<RelayView> {
  const body = await readJSON<JsonResponse<"setDomainRelay", 200>>(
    await apiV1.request(`/admin/domains/${domainId}/relay`, {
      method: "PUT",
      body: JSON.stringify(relay),
    }),
    "Could not save the relay settings",
  );
  return body.data;
}

/** Messages a source sent in the reports, split by the receiver's DMARC result. */
export interface DmarcSourceCounts {
  source: string;
  pass: number;
  fail: number;
}

/**
 * Totals each sending IP across the reports. A message passes DMARC when the
 * receiver evaluated DKIM or SPF as passing (RFC 7489 §6.6.2); sources with
 * failures come first, since they are what tightening the policy would affect.
 */
export function dmarcCountsBySource(reports: DmarcReport[]): DmarcSourceCounts[] {
  const bySource = new Map<string, DmarcSourceCounts>();
  for (const report of reports) {
    for (const { row } of report.records) {
      const counts = bySource.get(row.source_ip) ?? {
        source: row.source_ip,
        pass: 0,
        fail: 0,
      };
      const evaluated = row.policy_evaluated;
      if (evaluated.dkim === "pass" || evaluated.spf === "pass") {
        counts.pass += row.count;
      } else {
        counts.fail += row.count;
      }
      bySource.set(row.source_ip, counts);
    }
  }
  return [...bySource.values()].sort(
    (a, b) => b.fail - a.fail || b.pass - a.pass || a.source.localeCompare(b.source),
  );
}
