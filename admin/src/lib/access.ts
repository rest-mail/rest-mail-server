import type { components } from "./api-types";

// What the signed-in session holds, as the API reported it at sign-in or
// refresh: `capabilities` are held at "*" (the whole server), and
// `scopedCapabilities` at a narrower scope such as `domain:customer.test`.
// This decides only what the app shows; the API checks every request itself.
export interface SessionAccess {
  capabilities: string[];
  scopedCapabilities?: components["schemas"]["Entry"][];
}

// Need is what one screen's API route requires, read from the route's
// registration (internal/api/routes.go and the handlers' Register): `server`
// routes check the capability at "*" (`needs`/`checks.At`), `anywhere` routes
// accept it at any scope and filter what they answer (`checks.OnList`).
export type Need = { capability: string; at: "server" | "anywhere" };

const WILDCARD = "*";

export function holds(access: SessionAccess | null | undefined, need: Need): boolean {
  if (!access) return false;
  const global = access.capabilities ?? [];
  if (global.includes(WILDCARD) || global.includes(need.capability)) return true;
  if (need.at === "server") return false;
  return (access.scopedCapabilities ?? []).some(
    (entry) => entry.capability === WILDCARD || entry.capability === need.capability,
  );
}

const server = (capability: string): Need => ({ capability, at: "server" });
const anywhere = (capability: string): Need => ({ capability, at: "anywhere" });

// The route each screen loads first, and what that route requires. Paths are
// matched by longest prefix; a path not listed (sign-in, the dashboard, your
// own two-factor settings) needs nothing beyond a session.
const screenNeeds: [path: string, need: Need][] = [
  ["/domains/new", server("domains:write")], // POST /admin/domains
  ["/domains", anywhere("domains:read")], // GET /admin/domains
  ["/mailboxes", anywhere("accounts:read")], // GET /admin/mailboxes
  ["/aliases", anywhere("accounts:read")], // GET /admin/aliases
  ["/queue", server("queue:read")], // GET /admin/queue, /admin/queue/stats
  ["/pipelines/logs", server("pipelines:read")], // GET /admin/pipelines/logs
  ["/pipelines", anywhere("pipelines:read")], // GET /admin/pipelines
  ["/custom-filters", anywhere("pipelines:read")], // GET /admin/custom-filters
  ["/messages", server("observability:read")], // GET /admin/messages/{id}/trace
  ["/grants", server("users:read")], // GET /admin/grants, /admin/roles
  ["/api-keys", anywhere("accounts:read")], // GET /admin/api-keys
  ["/settings/limits", server("users:read")], // GET /admin/server-limits
  ["/settings/dkim", server("domains:read")], // GET /admin/dkim
  ["/settings/certificates", server("domains:read")], // GET /admin/certificates
  ["/settings/bans", server("bans:read")], // GET /admin/bans
  ["/settings/tls-reports", anywhere("domains:read")], // GET /admin/tls-reports
  ["/settings/mta-sts", anywhere("domains:read")], // GET /admin/domains, then per domain
];

function matches(pathname: string, prefix: string): boolean {
  return pathname === prefix || pathname.startsWith(prefix + "/");
}

// needOf is what reaching pathname requires, or null when a session is enough.
export function needOf(pathname: string): Need | null {
  let best: [string, Need] | null = null;
  for (const entry of screenNeeds) {
    if (matches(pathname, entry[0]) && (!best || entry[0].length > best[0].length)) {
      best = entry;
    }
  }
  return best ? best[1] : null;
}

export function canReach(access: SessionAccess | null | undefined, pathname: string): boolean {
  const need = needOf(pathname);
  return need === null || holds(access, need);
}

// Whole-server panels of the dashboard.
export const dashboardNeeds = {
  stats: server("domains:read"), // GET /admin/stats
  funnel: server("observability:read"), // GET /admin/pipelines/analytics
  domains: anywhere("domains:read"), // GET /admin/domains, /dmarc-reports, /tls-reports
} as const;
