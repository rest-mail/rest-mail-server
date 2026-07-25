/**
 * API client utility for REST Mail admin
 */

const API_BASE = import.meta.env.VITE_API_URL

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
     * @param _token - Deprecated no-op. The token now rides in the httpOnly
     *   cookie; this parameter is retained only so existing call sites compile
     *   unchanged, and its value is ignored.
     * @returns Fetch response
     */
    request: async (
      path: string,
      options: RequestInit = {},
      _token?: string
    ): Promise<Response> => {
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
 * API client using VITE_API_URL environment variable
 */
export const apiV1 = createApiClient()

/**
 * Legacy exports for backward compatibility
 * @deprecated Use apiV1.url() and apiV1.request() instead
 */
export const apiUrl = apiV1.url
export const apiRequest = apiV1.request
