import { useId } from "react";
import { DMARC_POLICIES } from "../../lib/domainPolicy";
import type { DmarcPolicy } from "../../lib/domainPolicy";

const labels: Record<DmarcPolicy, string> = {
  none: "none: monitor only",
  quarantine: "quarantine: failing mail goes to spam",
  reject: "reject: failing mail is refused",
};

interface DmarcPolicyFieldProps {
  value: DmarcPolicy;
  onChange: (policy: DmarcPolicy) => void;
}

/** The domain's DMARC policy on the edit form, with how to move it safely. */
export function DmarcPolicyField({ value, onChange }: DmarcPolicyFieldProps) {
  const id = useId();
  const helpId = `${id}-help`;
  return (
    <div className="flex flex-col gap-2">
      <label
        htmlFor={id}
        className="text-sm font-medium"
        style={{ color: "var(--black-soft)" }}
      >
        DMARC policy
      </label>
      <select
        id={id}
        value={value}
        aria-describedby={helpId}
        onChange={(e) => onChange(e.target.value as DmarcPolicy)}
        className="h-10 px-3 border text-sm"
        style={{ borderColor: "var(--gray-border)", color: "var(--black-soft)" }}
      >
        {DMARC_POLICIES.map((policy) => (
          <option key={policy} value={policy}>
            {labels[policy]}
          </option>
        ))}
      </select>
      <div id={helpId} className="flex flex-col gap-2">
        <p className="text-xs" style={{ color: "var(--gray-secondary)" }}>
          Start at none and read the DMARC reports below. Once every source
          that sends for this domain passes, move to quarantine, and later to
          reject.
        </p>
        <p
          className="text-xs p-3 border"
          style={{
            borderColor: "#F59E0B",
            backgroundColor: "#FFFBEB",
            color: "#92400E",
          }}
        >
          Saving changes the DMARC record that <code>chore dns:show</code>{" "}
          prints, not the one in DNS. Publish the new record at your DNS
          provider for receivers to apply it.
        </p>
      </div>
    </div>
  );
}
