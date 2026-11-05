import type { JsonResponse } from "../../lib/api-contract";

type DNSRecordCheck = JsonResponse<
  "domainDNSCheck",
  200
>["data"]["records"][number];
export type DKIMProblem = NonNullable<DNSRecordCheck["problem"]>;

// One plain-language line per DKIM problem the DNS check reports.
export const DKIM_PROBLEM_TEXT: Record<DKIMProblem, string> = {
  missing: "No DKIM record is published yet.",
  malformed:
    "The record published at this name is not a valid DKIM key record.",
  different_key_type:
    "The published record is for a different key type than the one this server signs with.",
  different_key: "The published record carries a different key.",
  more_than_one:
    "Other TXT records share this name, and receiving servers may not use the key. Keep only this record.",
  key_unreadable:
    "The server cannot read this domain's DKIM key, so mail for this domain is not being sent until the key is fixed.",
};
