import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useCallback, useEffect, useRef, useState } from 'react'
import { EditAPIKeyPermissions } from '../../components/apiKeys/EditAPIKeyPermissions'
import { AppShell } from '../../components/layout/AppShell'
import { type AdminAPIKey, type APIKeyFilters, type APIKeyPage, type APIKeyPermission, listAdminAPIKeys, revokeAPIKey } from '../../lib/apiKeys'
import { useAuthStore } from '../../lib/stores/authStore'

export const Route = createFileRoute('/api-keys/')({
  component: APIKeysPage,
})

const PAGE_SIZE = 50
const EMPTY_PAGE: APIKeyPage = { keys: [], total: 0, hasMore: false }

function when(time: string | null, none: string) {
  return time ? new Date(time).toLocaleString() : none
}

// Every API key the administrator's grants reach, whichever account it belongs to.
// Secrets are never shown: the server gives one out only when the key is made.
function APIKeysPage() {
  const { accessToken, logout } = useAuthStore()
  const navigate = useNavigate()
  const [draft, setDraft] = useState<APIKeyFilters>({})
  const [filters, setFilters] = useState<APIKeyFilters>({})
  const [offset, setOffset] = useState(0)
  const [page, setPage] = useState<APIKeyPage>(EMPTY_PAGE)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [editing, setEditing] = useState<AdminAPIKey | null>(null)
  const latest = useRef(0)

  const load = useCallback(async () => {
    // Only the newest request's answer is shown, whichever arrives last.
    const request = ++latest.current
    setIsLoading(true)
    try {
      const next = await listAdminAPIKeys(filters, PAGE_SIZE, offset)
      if (request !== latest.current) return
      // Revoking a page's last key, or editing it out of a permission filter, empties that page.
      if (next.keys.length === 0 && offset > 0) setOffset(Math.max(0, offset - PAGE_SIZE))
      else setPage(next)
    } catch (err) {
      if (request === latest.current) setError(err instanceof Error ? err.message : 'Could not load the API keys')
    } finally {
      if (request === latest.current) setIsLoading(false)
    }
  }, [filters, offset])

  useEffect(() => {
    if (accessToken) void load()
  }, [accessToken, load])

  const handleFilter = (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setOffset(0)
    setFilters({ ...draft })
  }

  const handleRevoke = async (key: AdminAPIKey) => {
    if (!confirm(`Revoke the API key "${key.name}" of ${key.account_address}? Anything using it stops working at once.`)) return
    setError('')
    setNotice('')
    try {
      await revokeAPIKey(key)
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not revoke the API key')
    }
  }

  const handleSaved = (key: AdminAPIKey, permissions: APIKeyPermission[]) => {
    setEditing(null)
    setError('')
    setNotice(`Saved the permissions of "${key.name}". Its secret has not changed.`)
    // Show what the server stored at once, so a list that then fails to reload is still right.
    setPage((current) => ({
      ...current,
      keys: current.keys.map((row) => (row.id === key.id ? { ...row, permissions } : row)),
    }))
    void load()
  }

  // A new sign-in is the only way to a new two-factor proof: refreshing a session keeps the old one.
  const handleSignInAgain = () => {
    logout()
    void navigate({ to: '/login' })
  }

  const field = 'h-10 px-3 border text-sm rounded'
  const fieldStyle = { borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }
  const labelStyle = { color: 'var(--gray-secondary)' }
  const set = (name: keyof APIKeyFilters) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setDraft((current) => ({ ...current, [name]: e.target.value }))

  return (
    <AppShell title="API keys">
      <div className="flex flex-col gap-6">
        <p className="text-sm" style={labelStyle}>
          The API keys of the accounts you can read, newest first. Revoking one needs a session signed in with two-factor
          authentication and access to manage its account. Changing its permissions also needs a recent two-factor sign-in
          and your current password.
        </p>

        {notice && (
          <div role="status" className="p-4 border flex items-center justify-between rounded" style={{ borderColor: '#22C55E', backgroundColor: '#F0FDF4' }}>
            <span className="text-sm" style={{ color: '#166534' }}>{notice}</span>
            <button type="button" onClick={() => setNotice('')} className="text-sm" style={{ color: '#166534' }}>
              Dismiss
            </button>
          </div>
        )}

        {error && (
          <div className="p-4 border flex items-center justify-between rounded" style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2' }}>
            <span className="text-sm" style={{ color: '#B91C1C' }}>{error}</span>
            <button type="button" onClick={() => setError('')} className="text-sm" style={{ color: '#B91C1C' }}>
              Dismiss
            </button>
          </div>
        )}

        <form onSubmit={handleFilter} className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Owner
            <input type="email" className={field} style={fieldStyle} value={draft.owner ?? ''} onChange={set('owner')} placeholder="someone@example.com" />
          </label>
          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Permission
            <input className={field} style={fieldStyle} value={draft.capability ?? ''} onChange={set('capability')} placeholder="mail:read" />
          </label>
          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Last used before
            <input type="date" className={field} style={fieldStyle} value={draft.lastUsedBefore ?? ''} onChange={set('lastUsedBefore')} />
          </label>
          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Expires before
            <input type="date" className={field} style={fieldStyle} value={draft.expiresBefore ?? ''} onChange={set('expiresBefore')} />
          </label>
          <button type="submit" className="h-10 px-5 text-white text-sm font-medium rounded" style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}>
            Filter
          </button>
        </form>

        <table className="w-full text-sm">
          <thead>
            <tr className="text-left" style={labelStyle}>
              <th className="py-2">Owner</th>
              <th className="py-2">Name</th>
              <th className="py-2">Permissions</th>
              <th className="py-2">Last used</th>
              <th className="py-2">Expires</th>
              <th className="py-2" />
            </tr>
          </thead>
          <tbody>
            {page.keys.length === 0 ? (
              <tr>
                <td colSpan={6} className="py-4" style={labelStyle}>
                  {isLoading ? 'Loading…' : 'No API keys.'}
                </td>
              </tr>
            ) : (
              page.keys.map((key) => (
                <tr key={key.id} className="border-t" style={{ borderColor: 'var(--gray-border)' }}>
                  <td className="py-2">{key.account_address}</td>
                  <td className="py-2">{key.name}</td>
                  <td className="py-2">
                    {key.permissions.map((p) => (
                      <div key={`${p.capability} ${p.scope}`}>
                        <code>{p.capability}</code> at <code>{p.scope}</code>
                      </div>
                    ))}
                  </td>
                  <td className="py-2">{when(key.last_used_at, 'Never')}</td>
                  <td className="py-2">{when(key.expires_at, 'Never')}</td>
                  <td className="py-2 text-right whitespace-nowrap">
                    <button type="button" onClick={() => { setNotice(''); setEditing(key) }} className="text-sm mr-3" style={{ color: 'var(--black-soft)' }}>
                      Edit
                    </button>
                    <button type="button" onClick={() => handleRevoke(key)} className="text-sm" style={{ color: '#B91C1C' }}>
                      Revoke
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>

        {page.total > 0 && (
          <div className="flex items-center justify-between text-sm" style={labelStyle}>
            <span>
              {offset + 1}–{offset + page.keys.length} of {page.total}
            </span>
            <span className="flex gap-3">
              <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))} className="disabled:opacity-50">
                Previous
              </button>
              <button type="button" disabled={!page.hasMore} onClick={() => setOffset(offset + PAGE_SIZE)} className="disabled:opacity-50">
                Next
              </button>
            </span>
          </div>
        )}
      </div>

      {editing && (
        <EditAPIKeyPermissions
          key={editing.id}
          apiKey={editing}
          onSaved={(permissions) => handleSaved(editing, permissions)}
          onClose={() => setEditing(null)}
          onGone={() => void load()}
          onSignInAgain={handleSignInAgain}
        />
      )}
    </AppShell>
  )
}
