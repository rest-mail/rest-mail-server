/**
 * The administrator's view of API keys across accounts (#1387). A key's secret
 * is never in these answers: the server shows it once, when the key is made.
 */
import { apiV1 } from "./api";
import { expectSuccess, readJSON } from "./api-contract";

export interface AdminAPIKey {
  id: number;
  name: string;
  kind: string;
  public_id: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
  permissions: { capability: string; scope: string }[];
  account_id: number;
  account_address: string;
}

export interface APIKeyFilters {
  owner?: string;
  capability?: string;
  /** A date from a date field; keys last used before it, and never-used ones. */
  lastUsedBefore?: string;
  /** A date from a date field; keys that expire before it. */
  expiresBefore?: string;
}

export interface APIKeyPage {
  keys: AdminAPIKey[];
  total: number;
  hasMore: boolean;
}

export function apiKeysQuery(filters: APIKeyFilters, limit: number, offset: number): string {
  const query = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  const owner = filters.owner?.trim();
  const capability = filters.capability?.trim();
  if (owner) query.set("owner", owner);
  if (capability) query.set("capability", capability);
  if (filters.lastUsedBefore) query.set("last_used_before", new Date(filters.lastUsedBefore).toISOString());
  if (filters.expiresBefore) query.set("expires_before", new Date(filters.expiresBefore).toISOString());
  return query.toString();
}

export async function listAdminAPIKeys(filters: APIKeyFilters, limit: number, offset: number): Promise<APIKeyPage> {
  const body = await readJSON<{
    data: AdminAPIKey[];
    pagination?: { total: number; has_more: boolean };
  }>(await apiV1.request(`/admin/api-keys?${apiKeysQuery(filters, limit, offset)}`), "Could not load the API keys");
  return {
    keys: body.data,
    total: body.pagination?.total ?? body.data.length,
    hasMore: body.pagination?.has_more ?? false,
  };
}

/** Revokes through the key's own account: the server checks access to that account. */
export async function revokeAPIKey(key: Pick<AdminAPIKey, "id" | "account_id">): Promise<void> {
  await expectSuccess(
    await apiV1.request(`/accounts/${key.account_id}/api-keys/${key.id}`, { method: "DELETE" }),
    "Could not revoke the API key",
  );
}
