/**
 * API client utility for REST Mail admin
 */

export const DEFAULT_API_BASE = '/api/v1'

/**
 * Resolve the REST API base URL.
 *
 * The API is same-origin with the admin app: #257 moved auth to httpOnly
 * cookies and the nginx CSP is `connect-src 'self'`, so every request must stay
 * on the app's own origin (a cross-origin base would also drop the
 * SameSite=Strict session cookie). The default base is therefore a
 * root-relative path — correct-by-construction in every deployment, and never
 * `undefined`.
 *
 * Previously `API_BASE` was `import.meta.env.VITE_API_URL` verbatim. In prod the
 * value is never baked (the image build passes no VITE_API_URL build-arg), so
 * `API_BASE` was `undefined` and every request went to `undefined/...`. That
 * soft failure is what this resolver removes.
 *
 * VITE_API_URL may still override the default (e.g. a cross-origin dev backend),
 * but only with a valid value — a root-relative path or an absolute http(s)
 * URL. A set-but-malformed value fails loud at module load rather than building
 * broken request URLs.
 *
 * - unset / blank / `"/"` → {@link DEFAULT_API_BASE}
 * - a root-relative path or absolute http(s) URL → used as-is (trailing slash trimmed)
 * - anything else (a bare host, the literal `"undefined"`, …) → throws
 */
export function resolveApiBase(raw: string | undefined): string {
  const base = (raw ?? '').trim().replace(/\/+$/, '')
  if (base === '') return DEFAULT_API_BASE
  if (!base.startsWith('/') && !/^https?:\/\/\S+$/i.test(base)) {
    throw new Error(
      `Invalid VITE_API_URL: ${JSON.stringify(raw)}. Expected a root-relative ` +
        `path such as "/api/v1" or an absolute http(s) URL; unset it to use the ` +
        `same-origin default.`,
    )
  }
  return base
}

const API_BASE = resolveApiBase(import.meta.env.VITE_API_URL)

// Global 401 handler
let unauthorizedHandler: (() => void) | null = null

/**
 * Set a global handler for 401 responses
 * @param handler - Function to call when 401 is detected
 */
export function setUnauthorizedHandler(handler: () => void) {
  unauthorizedHandler = handler
}

const MUTATING_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE'])

/**
 * Read the readable (non-httpOnly) CSRF token the API set at login/refresh. The
 * access token itself lives in an httpOnly cookie JavaScript cannot read; this
 * companion value is echoed in the X-CSRF-Token header on mutating requests.
 */
export function getCsrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)restmail_csrf=([^;]*)/)
  return match ? decodeURIComponent(match[1]) : ''
}

/**
 * Creates an API client
 */
function createApiClient() {
  return {
    /**
     * Constructs a full API URL from a path
     * @param path - API path (e.g., '/admin/domains')
     * @returns Full API URL
     */
    url: (path: string): string => {
      return `${API_BASE}${path}`
    },

    /**
     * Makes an authenticated API request.
     *
     * Authentication is by cookie: the browser attaches the httpOnly
     * `restmail_access` cookie automatically (credentials: 'include'), so the
     * token is never held in JavaScript. State-changing requests carry the
     * double-submit CSRF token in the X-CSRF-Token header.
     *
     * @param path - API path
     * @param options - Fetch options
     * @returns Fetch response
     *
     * A third positional argument — a legacy access token — is still accepted
     * from existing call sites but ignored: the access token now rides in the
     * httpOnly `restmail_access` cookie the browser attaches automatically
     * (credentials: 'include'), so it is never held in JavaScript. It is
     * absorbed as an unused rest parameter (and discarded) so those call sites
     * keep compiling without a dead named parameter.
     */
    request: async (
      path: string,
      options: RequestInit = {},
      ...ignoredLegacyToken: [token?: string]
    ): Promise<Response> => {
      void ignoredLegacyToken
      const headers = new Headers(options.headers)

      if (!headers.has('Content-Type') && options.body) {
        headers.set('Content-Type', 'application/json')
      }

      const method = (options.method || 'GET').toUpperCase()
      if (MUTATING_METHODS.has(method)) {
        headers.set('X-CSRF-Token', getCsrfToken())
      }

      const response = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers,
        credentials: 'include',
      })

      // Handle 401 unauthorized responses
      if (response.status === 401 && unauthorizedHandler) {
        unauthorizedHandler()
      }

      return response
    },
  }
}

/**
 * API client rooted at the resolved same-origin API base (see resolveApiBase).
 */
export const apiV1 = createApiClient()

/**
 * Legacy exports for backward compatibility
 * @deprecated Use apiV1.url() and apiV1.request() instead
 */
export const apiUrl = apiV1.url
export const apiRequest = apiV1.request
