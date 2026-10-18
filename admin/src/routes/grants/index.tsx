import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useGrantStore } from '../../lib/stores/grantStore'
import { useMailboxStore } from '../../lib/stores/mailboxStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/grants/')({
  component: GrantsPage,
})

// Every account is a mailbox; an admin is an account holding admin grants.
// This page lists who holds which role where, and grants and removes them.
function GrantsPage() {
  const { accessToken } = useAuthStore()
  const { grants, roles, isLoading, error, fetchGrants, fetchRoles, createGrant, deleteGrant, clearError } =
    useGrantStore()
  const { mailboxes, fetchMailboxes } = useMailboxStore()
  const [accountId, setAccountId] = useState<number | ''>('')
  const [role, setRole] = useState('')
  const [scope, setScope] = useState('*')
  const [mode, setMode] = useState<'active' | 'eligible'>('active')

  useEffect(() => {
    if (!accessToken) return
    fetchGrants()
    fetchRoles()
    fetchMailboxes().catch(() => {})
  }, [accessToken, fetchGrants, fetchRoles, fetchMailboxes])

  const addressOf = useMemo(() => {
    const byId = new Map<number, string>()
    for (const mb of mailboxes) byId.set(mb.id, mb.address)
    return (id: number) => byId.get(id) || `account ${id}`
  }, [mailboxes])

  const handleGrant = async (e: React.FormEvent) => {
    e.preventDefault()
    if (accountId === '' || !role) return
    try {
      await createGrant({ accountId, role, scope: scope.trim(), mode })
      setAccountId('')
    } catch {
      // The store shows the error.
    }
  }

  const handleDelete = async (id: number) => {
    try {
      await deleteGrant(id)
    } catch {
      // The store shows the error.
    }
  }

  const field = 'h-10 px-3 border text-sm rounded'
  const fieldStyle = { borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }

  return (
    <AppShell title="Grants">
      <div className="flex flex-col gap-6">
        <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          Which account holds which role, at which scope: <code>*</code>, <code>domain:example.com</code> or{' '}
          <code>mailbox:someone@example.com</code>. Changing grants needs a session signed in with two-factor
          authentication, and you can only grant what you hold.
        </p>

        {error && (
          <div className="p-4 border flex items-center justify-between rounded" style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2' }}>
            <span className="text-sm" style={{ color: '#B91C1C' }}>{error}</span>
            <button type="button" onClick={clearError} className="text-sm" style={{ color: '#B91C1C' }}>
              Dismiss
            </button>
          </div>
        )}

        <form onSubmit={handleGrant} className="flex flex-wrap items-end gap-3">
          <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
            Account
            <select
              className={field}
              style={fieldStyle}
              value={accountId}
              onChange={(e) => setAccountId(e.target.value === '' ? '' : Number(e.target.value))}
            >
              <option value="">Choose…</option>
              {mailboxes.map((mb) => (
                <option key={mb.id} value={mb.id}>
                  {mb.address}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
            Role
            <select className={field} style={fieldStyle} value={role} onChange={(e) => setRole(e.target.value)}>
              <option value="">Choose…</option>
              {roles.map((r) => (
                <option key={r.id} value={r.name} title={r.capabilities.join(', ')}>
                  {r.name}
                </option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
            Scope
            <input className={field} style={fieldStyle} value={scope} onChange={(e) => setScope(e.target.value)} />
          </label>
          <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
            Mode
            <select
              className={field}
              style={fieldStyle}
              value={mode}
              onChange={(e) => setMode(e.target.value === 'eligible' ? 'eligible' : 'active')}
            >
              <option value="active">active</option>
              <option value="eligible">eligible (switched on when needed)</option>
            </select>
          </label>
          <button
            type="submit"
            disabled={accountId === '' || !role}
            className="h-10 px-5 text-white text-sm font-medium rounded disabled:opacity-50"
            style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
          >
            Grant
          </button>
        </form>

        <table className="w-full text-sm">
          <thead>
            <tr className="text-left" style={{ color: 'var(--gray-secondary)' }}>
              <th className="py-2">Account</th>
              <th className="py-2">Role</th>
              <th className="py-2">Scope</th>
              <th className="py-2">Mode</th>
              <th className="py-2">Expires</th>
              <th className="py-2" />
            </tr>
          </thead>
          <tbody>
            {isLoading && grants.length === 0 ? (
              <tr>
                <td colSpan={6} className="py-4" style={{ color: 'var(--gray-secondary)' }}>
                  Loading…
                </td>
              </tr>
            ) : (
              grants.map((g) => (
                <tr key={g.id} className="border-t" style={{ borderColor: 'var(--gray-border)' }}>
                  <td className="py-2">{addressOf(g.holder.id)}</td>
                  <td className="py-2">{g.role}</td>
                  <td className="py-2"><code>{g.scope}</code></td>
                  <td className="py-2">{g.mode}</td>
                  <td className="py-2">{g.expires_at ? new Date(g.expires_at).toLocaleString() : '—'}</td>
                  <td className="py-2 text-right">
                    <button type="button" onClick={() => handleDelete(g.id)} className="text-sm" style={{ color: '#B91C1C' }}>
                      Remove
                    </button>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
    </AppShell>
  )
}
