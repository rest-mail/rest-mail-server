import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/$id')({
  component: EditAliasPage,
})

function EditAliasPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { accessToken } = useAuthStore()
  const { aliases, fetchAliases, updateAlias } = useAliasStore()

  const alias = aliases.find((a) => a.id.toString() === id)

  const [formData, setFormData] = useState({
    destination_address: '',
    active: true,
  })
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [isSubmitting, setIsSubmitting] = useState(false)

  useEffect(() => {
    if (accessToken && !alias) {
      fetchAliases(accessToken)
    }
  }, [accessToken, alias, fetchAliases])

  useEffect(() => {
    if (alias) {
      setFormData({
        destination_address: alias.destination_address,
        active: alias.active,
      })
    }
  }, [alias])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!accessToken || !alias) return

    setErrors({})
    setIsSubmitting(true)

    try {
      await updateAlias(accessToken, alias.id, formData)
      navigate({ to: '/aliases' })
    } catch (err) {
      console.error('Failed to update alias:', err)
      setErrors({ submit: err instanceof Error ? err.message : 'Failed to update alias' })
    } finally {
      setIsSubmitting(false)
    }
  }

  if (!alias) {
    return (
      <AppShell title="Edit Alias">
        <div className="flex items-center justify-center py-20">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Loading alias...
          </div>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title={`Edit Alias: ${alias.source_address}`}>
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
              {/* Source Address (Read-only) */}
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Source Address
                </label>
                <div
                  className="w-full h-11 px-4 flex items-center border text-sm"
                  style={{ borderColor: 'var(--gray-border)', backgroundColor: '#F9FAFB', color: 'var(--gray-secondary)' }}
                >
                  {alias.source_address}
                </div>
                <p className="mt-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Source address cannot be changed
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
              </div>

              {/* Active Status */}
              <div>
                <label className="flex items-center gap-3 cursor-pointer">
                  <input
                    type="checkbox"
                    checked={formData.active}
                    onChange={(e) => setFormData({ ...formData, active: e.target.checked })}
                    className="w-4 h-4"
                    style={{ accentColor: 'var(--red-primary)' }}
                  />
                  <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Active
                  </span>
                </label>
                <p className="mt-1 ml-7 text-xs" style={{ color: 'var(--gray-secondary)' }}>
                  Inactive aliases will not forward mail
                </p>
              </div>

              {/* Metadata */}
              <div className="pt-4 border-t" style={{ borderColor: 'var(--gray-border)' }}>
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <span style={{ color: 'var(--gray-secondary)' }}>Domain:</span>
                    <span className="ml-2 font-medium" style={{ color: 'var(--black-soft)' }}>
                      {alias.domain?.name || '—'}
                    </span>
                  </div>
                  <div>
                    <span style={{ color: 'var(--gray-secondary)' }}>Created:</span>
                    <span className="ml-2 font-medium" style={{ color: 'var(--black-soft)' }}>
                      {new Date(alias.created_at).toLocaleDateString()}
                    </span>
                  </div>
                </div>
              </div>

              {/* Actions */}
              <div className="flex gap-3 pt-4">
                <button
                  type="submit"
                  disabled={isSubmitting}
                  className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded disabled:opacity-50"
                  style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
                >
                  {isSubmitting ? 'Saving...' : 'Save Changes'}
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
