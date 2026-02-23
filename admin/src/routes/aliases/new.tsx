import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/new')({
  component: CreateAliasPage,
})

function CreateAliasPage() {
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { createAlias } = useAliasStore()

  const [formData, setFormData] = useState({
    source_address: '',
    destination_address: '',
  })
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isSubmitting, setIsSubmitting] = useState(false)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken) return

    setErrors({})
    setIsSubmitting(true)

    try {
      await createAlias(accessToken, formData)
      navigate({ to: '/aliases' })
    } catch (err) {
      console.error('Failed to create alias:', err)
      setErrors({ submit: err instanceof Error ? err.message : 'Failed to create alias' })
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <AppShell title="Create Alias">
      <div className="max-w-2xl">
        <div className="mb-6">
          <button
            onClick={() => navigate({ to: '/aliases' })}
            className="text-sm hover:underline"
            style={{ color: 'var(--gray-secondary)' }}
          >
            ← Back to aliases
          </button>
        </div>

        <div className="bg-white border" style={{ borderColor: 'var(--gray-border)' }}>
          <div className="px-6 py-4 border-b" style={{ borderColor: 'var(--gray-border)' }}>
            <h2 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
              Alias Details
            </h2>
          </div>

          <form onSubmit={handleSubmit} className="p-6">
            {/* Error Message */}
            {errors.submit && (
              <div
                className="p-4 mb-6 border text-sm"
                style={{
                  borderColor: '#EF4444',
                  backgroundColor: '#FEF2F2',
                  color: '#DC2626',
                }}
              >
                {errors.submit}
              </div>
            )}

            <div className="space-y-6">
              {/* Source Address */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Source Address
                </label>
                <input
                  type="email"
                  value={formData.source_address}
                  onChange={(e) => setFormData({ ...formData, source_address: e.target.value })}
                  placeholder="alias@example.com"
                  className="w-full h-11 px-4 border text-sm outline-none"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  required
                />
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  The email address that will receive forwarded mail
                </p>
              </div>

              {/* Destination Address */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Destination Address
                </label>
                <input
                  type="email"
                  value={formData.destination_address}
                  onChange={(e) => setFormData({ ...formData, destination_address: e.target.value })}
                  placeholder="user@example.com"
                  className="w-full h-11 px-4 border text-sm outline-none"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                  required
                />
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Where mail sent to the source address will be forwarded
                </p>
              </div>

              {/* Actions */}
              <div className="flex gap-3 pt-4">
                <button
                  type="submit"
                  disabled={isSubmitting}
                  className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
                  style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
                >
                  {isSubmitting ? 'Creating...' : 'Create Alias'}
                </button>
                <button
                  type="button"
                  onClick={() => navigate({ to: '/aliases' })}
                  className="h-11 px-6 flex items-center justify-center border text-sm font-medium rounded"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--gray-secondary)' }}
                >
                  Cancel
                </button>
              </div>
            </div>
          </form>
        </div>
      </div>
    </AppShell>
  )
}
