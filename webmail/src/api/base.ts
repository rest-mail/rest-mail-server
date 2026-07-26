// Resolve the REST API base URL.
//
// The API is same-origin with the webmail app: #257 moved auth to httpOnly
// cookies and the nginx CSP is `connect-src 'self'`, so every fetch and SSE
// stream must stay on the app's own origin (a cross-origin base would also drop
// the SameSite=Strict session cookie). The default base is therefore a
// root-relative path — correct-by-construction in every deployment, and never
// `undefined`.
//
// Previously a missing VITE_API_URL fell back to a hard-coded plaintext
// `http://localhost:8080/api/v1`. In prod the value is never baked (the image
// build passes no VITE_API_URL build-arg), so that fallback silently shipped,
// producing wrong-origin, non-TLS requests. That soft failure is what this
// module removes.
//
// VITE_API_URL may still override the default (e.g. a cross-origin dev backend),
// but only with a valid value — a root-relative path or an absolute http(s)
// URL. A set-but-malformed value fails loud at module load rather than building
// broken `undefined/...` (or bare-host) request URLs.

export const DEFAULT_API_BASE = '/api/v1';

/**
 * Normalise the configured API base, defaulting to the same-origin path.
 *
 * - unset / blank / `"/"` → {@link DEFAULT_API_BASE} (same-origin, correct-by-construction)
 * - a root-relative path or absolute http(s) URL → used as-is (trailing slash trimmed)
 * - anything else (a bare host, the literal `"undefined"`, …) → throws
 */
export function resolveApiBase(raw: string | undefined): string {
  const base = (raw ?? '').trim().replace(/\/+$/, '');
  if (base === '') return DEFAULT_API_BASE;
  if (!base.startsWith('/') && !/^https?:\/\/\S+$/i.test(base)) {
    throw new Error(
      `Invalid VITE_API_URL: ${JSON.stringify(raw)}. Expected a root-relative ` +
        `path such as "/api/v1" or an absolute http(s) URL; unset it to use the ` +
        `same-origin default.`,
    );
  }
  return base;
}

export const API_BASE = resolveApiBase(import.meta.env.VITE_API_URL);
