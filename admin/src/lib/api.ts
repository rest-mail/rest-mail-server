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
     * Makes an authenticated API request
     * @param path - API path
     * @param options - Fetch options
     * @param token - Optional auth token
     * @returns Fetch response
     */
    request: async (
      path: string,
      options: RequestInit = {},
      token?: string
    ): Promise<Response> => {
      const headers = new Headers(options.headers)

      if (token) {
        headers.set('Authorization', `Bearer ${token}`)
      }

      if (!headers.has('Content-Type') && options.body) {
        headers.set('Content-Type', 'application/json')
      }

      const response = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers,
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
