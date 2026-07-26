import { Outlet, createRootRoute, useNavigate } from '@tanstack/react-router'
import { lazy, Suspense, useEffect } from 'react'
import { setUnauthorizedHandler } from '../lib/api'
import { useAuthStore } from '../lib/stores/authStore'

import '../styles.css'

// Devtools are loaded ONLY in development, via a dev-gated dynamic import. In a
// production build `import.meta.env.DEV` is statically `false`, so the ternary
// folds to the no-op component and the `import()` (with the devtools packages)
// is dropped from the bundle entirely — they never ship to users.
const DevTools = import.meta.env.DEV
  ? lazy(() =>
      import('../components/DevTools').then((m) => ({ default: m.DevTools })),
    )
  : () => null

export const Route = createRootRoute({
  component: RootComponent,
  notFoundComponent: () => (
    <div className="min-h-screen bg-white flex items-center justify-center">
      <div className="text-center">
        <h1 className="text-4xl font-bold mb-4" style={{ color: 'var(--black-soft)' }}>
          404
        </h1>
        <p className="text-lg mb-4" style={{ color: 'var(--gray-secondary)' }}>
          Page not found
        </p>
        <a
          href="/admin"
          className="text-sm"
          style={{ color: 'var(--red-primary)' }}
        >
          Go to Dashboard
        </a>
      </div>
    </div>
  ),
})

function RootComponent() {
  const navigate = useNavigate()
  const logout = useAuthStore((state) => state.logout)
  const checkSession = useAuthStore((state) => state.checkSession)

  // Restore an existing session on boot by exchanging the httpOnly refresh
  // cookie for a fresh access cookie. No token is ever exposed to JS; on failure
  // the session is cleared and the route guards redirect to /login.
  useEffect(() => {
    void checkSession()
  }, [checkSession])

  // Set up global 401 handler
  useEffect(() => {
    setUnauthorizedHandler(() => {
      logout()
      navigate({ to: '/login' })
    })
  }, [logout, navigate])

  return (
    <>
      <Outlet />
      <Suspense fallback={null}>
        <DevTools />
      </Suspense>
    </>
  )
}
