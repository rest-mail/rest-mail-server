import { createFileRoute, Navigate } from '@tanstack/react-router'
import { useAuthStore } from '../lib/stores/authStore'

export const Route = createFileRoute('/')({
  component: IndexRedirect,
})

function IndexRedirect() {
  const { isAuthenticated } = useAuthStore()

  // Redirect to dashboard if authenticated, otherwise to login
  if (isAuthenticated) {
    return <Navigate to="/dashboard" />
  }

  return <Navigate to="/login" />
}
