/**
 * The administrator's view of API keys across accounts (#1387). A key's secret
 * is never in these answers: the server shows it once, when the key is made.
 */
import { apiV1 } from "./api";
import { expectSuccess, readJSON, type JsonResponse } from "./api-contract";

export interface APIKeyPermission {
  capability: string;
  scope: string;
}

export interface AdminAPIKey {
  id: number;
  name: string;
  kind: string;
  public_id: string;
  created_at: string;
  last_used_at: string | null;
  expires_at: string | null;
  permissions: APIKeyPermission[];
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

/** The names a permission can use. The server still decides what a key may hold. */
export async function listCapabilityNames(): Promise<string[]> {
  const body = await readJSON<JsonResponse<"listCapabilities", 200>>(
    await apiV1.request("/admin/capabilities"),
    "Could not load the capabilities",
  );
  return body.data.map((capability) => capability.name);
}

/** A refused change to a key's permissions. `code` is the API's error code. */
export class APIKeyRefusal extends Error {
  constructor(
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "APIKeyRefusal";
  }

  /** Only a new sign-in gets past this: nothing on the form is at fault. */
  get needsSignIn(): boolean {
    return (
      this.code === "recent_two_factor_required" ||
      this.code === "two_factor_required"
    );
  }
}

// What to tell the administrator for each refusal that has one cause. The password is the
// administrator's own, whichever account the key belongs to. "forbidden" is not here: it is how
// the server refuses permissions over the ceiling, but other checks answer with it too, so the
// server's own message is the one that cannot name the wrong cause.
const refusals: Record<string, string> = {
  recent_two_factor_required:
    "Your two-factor sign-in is no longer recent enough for this change. Sign in again with your code, then make the change again.",
  two_factor_required:
    "This change needs a session signed in with two-factor authentication. Sign in again with your code, then make the change again.",
  reauth_required: "Enter your current password to change this key's permissions.",
  invalid_password:
    "That is not your current password. Enter the password you sign in with, not the key owner's.",
  not_found:
    "This API key no longer exists, or you may not manage its account. Nothing was changed.",
};

async function toRefusal(response: Response): Promise<APIKeyRefusal> {
  const fallback = "Could not change the API key's permissions";
  try {
    const { error } = await response.json();
    const code: string = error?.code || "unknown";
    // A validation failure's message only says the body failed; its fields say why.
    const fields: Record<string, string> = error?.details?.fields ?? {};
    const reasons = Object.entries(fields).map(([field, reason]) => `${field}: ${reason}`);
    return new APIKeyRefusal(code, refusals[code] || reasons.join("; ") || error?.message || fallback);
  } catch {
    // A proxy or an empty error response may not carry the API's JSON envelope.
    return new APIKeyRefusal("unknown", fallback);
  }
}

/**
 * Replaces a key's permissions and returns them as the server stored them. The key's secret, name
 * and expiry stay as they are. `password` is the current password of the signed-in administrator.
 */
export async function updateAPIKeyPermissions(
  key: Pick<AdminAPIKey, "id" | "account_id">,
  password: string,
  permissions: APIKeyPermission[],
): Promise<APIKeyPermission[]> {
  const response = await apiV1.request(`/accounts/${key.account_id}/api-keys/${key.id}`, {
    method: "PATCH",
    body: JSON.stringify({ password, permissions }),
  });
  if (!response.ok) throw await toRefusal(response);
  const body: { data: { permissions: APIKeyPermission[] } } = await response.json();
  return body.data.permissions;
}
