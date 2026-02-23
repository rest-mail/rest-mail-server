import { Outlet, createRootRoute, useNavigate } from '@tanstack/react-router'
import { TanStackRouterDevtoolsPanel } from '@tanstack/react-router-devtools'
import { TanStackDevtools } from '@tanstack/react-devtools'
import { useEffect } from 'react'
import { setUnauthorizedHandler } from '../lib/api'
import { useAuthStore } from '../lib/stores/authStore'

import '../styles.css'

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
      <TanStackDevtools
        config={{
          position: 'bottom-right',
        }}
        plugins={[
          {
            name: 'TanStack Router',
            render: <TanStackRouterDevtoolsPanel />,
          },
        ]}
      />
    </>
  )
}
