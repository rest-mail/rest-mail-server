import { useEffect, useId, useRef, useState } from 'react'
import {
  type AdminAPIKey,
  type APIKeyPermission,
  APIKeyRefusal,
  listCapabilityNames,
  updateAPIKeyPermissions,
} from '../../lib/apiKeys'

interface Props {
  apiKey: AdminAPIKey
  onSaved: (permissions: APIKeyPermission[]) => void
  onClose: () => void
  /** The server no longer has this key where the list said it was. */
  onGone: () => void
  onSignInAgain: () => void
}

type Row = APIKeyPermission & { id: number }

const field = 'h-10 px-3 border text-sm rounded w-full'
const fieldStyle = { borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }
const labelStyle = { color: 'var(--gray-secondary)' }

// Changes what a key may do. The secret is never here: the list does not carry it, and this form
// sends only the permissions and the administrator's password.
export function EditAPIKeyPermissions({ apiKey, onSaved, onClose, onGone, onSignInAgain }: Props) {
  const nextRow = useRef(apiKey.permissions.length)
  const [rows, setRows] = useState<Row[]>(() => apiKey.permissions.map((p, id) => ({ ...p, id })))
  const [password, setPassword] = useState('')
  const [capabilities, setCapabilities] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [needsSignIn, setNeedsSignIn] = useState(false)
  const titleId = useId()
  const capabilityListId = useId()

  useEffect(() => {
    let current = true
    // Only suggestions: an administrator who may not read this list can still type a name.
    listCapabilityNames().then(
      (names) => { if (current) setCapabilities(names) },
      () => {},
    )
    return () => { current = false }
  }, [])

  const change = (id: number, name: keyof APIKeyPermission) => (e: React.ChangeEvent<HTMLInputElement>) =>
    setRows((current) => current.map((row) => (row.id === id ? { ...row, [name]: e.target.value } : row)))

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    setNeedsSignIn(false)
    try {
      const permissions = rows.map(({ capability, scope }) => ({ capability: capability.trim(), scope: scope.trim() }))
      onSaved(await updateAPIKeyPermissions(apiKey, password, permissions))
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not change the API key's permissions")
      if (err instanceof APIKeyRefusal) {
        setNeedsSignIn(err.needsSignIn)
        if (err.code === 'invalid_password') setPassword('')
        if (err.code === 'not_found') onGone()
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50">
      <div role="dialog" aria-modal="true" aria-labelledby={titleId} className="bg-white rounded-lg p-6 w-full max-w-2xl max-h-[90vh] overflow-auto">
        <div className="flex items-center justify-between mb-2">
          <h2 id={titleId} className="text-xl font-semibold" style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}>
            Permissions of “{apiKey.name}”
          </h2>
          <button type="button" aria-label="Close" onClick={onClose} className="text-xl" style={labelStyle}>
            ×
          </button>
        </div>
        <p className="text-sm mb-4" style={labelStyle}>
          Owner: {apiKey.account_address}. The new permissions replace the ones below and apply to its next request. Its
          secret, name and expiry stay as they are, so nothing using the key has to change.
        </p>

        {error && (
          <div role="alert" className="mb-4 p-4 border rounded flex items-center justify-between gap-3" style={{ borderColor: '#EF4444', backgroundColor: '#FEF2F2' }}>
            <span className="text-sm" style={{ color: '#B91C1C' }}>{error}</span>
            {needsSignIn && (
              <button type="button" onClick={onSignInAgain} className="text-sm font-medium whitespace-nowrap" style={{ color: '#B91C1C' }}>
                Sign in again
              </button>
            )}
          </div>
        )}

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          <datalist id={capabilityListId}>
            {capabilities.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>

          <div className="flex flex-col gap-2">
            {rows.map((row, index) => (
              <div key={row.id} className="flex items-end gap-2">
                <label className="flex-1 flex flex-col gap-1 text-xs" style={labelStyle}>
                  Capability {index + 1}
                  <input required list={capabilityListId} className={field} style={fieldStyle} value={row.capability} onChange={change(row.id, 'capability')} placeholder="mail:read" />
                </label>
                <label className="flex-1 flex flex-col gap-1 text-xs" style={labelStyle}>
                  Scope {index + 1}
                  <input required className={field} style={fieldStyle} value={row.scope} onChange={change(row.id, 'scope')} placeholder="mailbox:someone@example.com" />
                </label>
                <button
                  type="button"
                  aria-label={`Remove permission ${index + 1}`}
                  onClick={() => setRows((current) => current.filter(({ id }) => id !== row.id))}
                  className="h-10 text-sm"
                  style={{ color: '#B91C1C' }}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
          <div>
            <button
              type="button"
              onClick={() => setRows((current) => [...current, { id: nextRow.current++, capability: '', scope: '' }])}
              className="text-sm"
              style={{ color: 'var(--red-primary)' }}
            >
              Add a permission
            </button>
          </div>
          <p className="text-xs" style={labelStyle}>
            A scope is <code>*</code>, <code>domain:example.com</code> or <code>mailbox:someone@example.com</code>. A key can
            hold only what both you and its owner hold now, so a permission either of you has lost must be removed before
            the key can be saved.
          </p>

          <label className="flex flex-col gap-1 text-xs" style={labelStyle}>
            Your current password
            <input
              type="password"
              autoComplete="current-password"
              required
              className={field}
              style={fieldStyle}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </label>

          <div className="flex justify-end gap-3">
            <button type="button" onClick={onClose} className="h-10 px-5 text-sm" style={labelStyle}>
              Cancel
            </button>
            <button
              type="submit"
              disabled={busy}
              className="h-10 px-5 text-white text-sm font-medium rounded disabled:opacity-50"
              style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
            >
              {busy ? 'Saving…' : 'Save'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
