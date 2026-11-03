import { holdsCapability } from "../../lib/domainPolicy";
import { DomainDmarcReports } from "./DomainDmarcReports";
import { DomainRelay } from "./DomainRelay";
import { DomainSuppressions } from "./DomainSuppressions";

interface DomainPolicySectionsProps {
  domainId: number;
  capabilities: readonly string[] | undefined;
}

const box = "border p-6";
const boxStyle = { borderColor: "var(--gray-border)" };

/**
 * DMARC reports, suppressed addresses and the outbound relay of one domain. Each
 * shows only when the session holds the capability its route needs; the API
 * still checks it, and the domain scope, on every request.
 */
export function DomainPolicySections({ domainId, capabilities }: DomainPolicySectionsProps) {
  if (!holdsCapability(capabilities, "domains:read")) return null;
  const canWrite = holdsCapability(capabilities, "domains:write");
  return (
    <div className="grid grid-cols-1 lg:grid-cols-2 gap-8 mt-8">
      <div className={`${box} lg:col-span-2`} style={boxStyle}>
        <DomainDmarcReports domainId={domainId} />
      </div>
      <div className={box} style={boxStyle}>
        <DomainSuppressions domainId={domainId} canClear={canWrite} />
      </div>
      <div className={box} style={boxStyle}>
        <DomainRelay domainId={domainId} canEdit={canWrite} />
      </div>
    </div>
  );
}
