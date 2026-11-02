/** What an administrator can set for the whole server (#1453). Superadmin only. */
import { apiV1 } from "./api";
import { readJSON } from "./api-contract";

export interface ServerLimits {
  contact_import_max_bytes: number;
}

const MIB = 1024 * 1024;

export const bytesToMiB = (bytes: number): number => bytes / MIB;

/** A whole number of bytes, at least one, for a size in MiB; null when it isn't a usable size. */
export function miBToBytes(text: string): number | null {
  const size = Number(text);
  if (text.trim() === "" || !Number.isFinite(size) || size <= 0) return null;
  const bytes = Math.round(size * MIB);
  return Number.isSafeInteger(bytes) && bytes >= 1 ? bytes : null;
}

export async function getServerLimits(): Promise<ServerLimits> {
  const body = await readJSON<{ data: ServerLimits }>(await apiV1.request("/admin/server-limits"), "Could not load the server limits");
  return body.data;
}

export async function updateServerLimits(limits: ServerLimits): Promise<ServerLimits> {
  const body = await readJSON<{ data: ServerLimits }>(
    await apiV1.request("/admin/server-limits", { method: "PUT", body: JSON.stringify(limits) }),
    "Could not change the server limits",
  );
  return body.data;
}
