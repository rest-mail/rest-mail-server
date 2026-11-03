/**
 * Sender rules for the whole installation, which every domain inherits (#1576).
 * A block at either scope wins over an allow at either scope, so a domain cannot
 * allow a sender these rules block. Reading needs domains:read at "*", changing
 * them domains:write at "*".
 */
import { apiV1 } from "./api";
import { expectSuccess, readJSON } from "./api-contract";
import type { components } from "./api-types";

export type InstallationSenderRule = components["schemas"]["InstallationSenderRule"];
export type SenderListType = InstallationSenderRule["list_type"];

/** Whether the session's installation-wide capabilities include the capability. */
export function holdsInstallationWide(capabilities: readonly string[], capability: string): boolean {
  return capabilities.includes("*") || capabilities.includes(capability);
}

export async function listInstallationSenderRules(): Promise<InstallationSenderRule[]> {
  const body = await readJSON<{ data: InstallationSenderRule[] }>(
    await apiV1.request("/admin/sender-rules"),
    "Could not load the installation's sender rules",
  );
  return body.data;
}

export async function addInstallationSenderRule(rule: {
  pattern: string;
  list_type: SenderListType;
  reason?: string;
}): Promise<InstallationSenderRule> {
  const body = await readJSON<{ data: InstallationSenderRule }>(
    await apiV1.request("/admin/sender-rules", { method: "POST", body: JSON.stringify(rule) }),
    "Could not add the sender rule",
  );
  return body.data;
}

export async function removeInstallationSenderRule(id: number): Promise<void> {
  await expectSuccess(
    await apiV1.request(`/admin/sender-rules/${id}`, { method: "DELETE" }),
    "Could not remove the sender rule",
  );
}
