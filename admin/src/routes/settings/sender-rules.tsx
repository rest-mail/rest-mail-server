import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { AppShell } from '../../components/layout/AppShell'
import {
  addInstallationSenderRule,
  holdsInstallationWide,
  listInstallationSenderRules,
  removeInstallationSenderRule,
  type InstallationSenderRule,
  type SenderListType,
} from '../../lib/installationSenderRules'
import { useAuthStore } from '../../lib/stores/authStore'

export const Route = createFileRoute('/settings/sender-rules')({
  component: SenderRulesPage,
})

function SenderRulesPage() {
  const { accessToken, user } = useAuthStore()
  const capabilities = user?.capabilities ?? []
  const canRead = holdsInstallationWide(capabilities, 'domains:read')
  const canWrite = holdsInstallationWide(capabilities, 'domains:write')
  const [rules, setRules] = useState<InstallationSenderRule[]>([])
  const [pattern, setPattern] = useState('')
  const [listType, setListType] = useState<SenderListType>('block')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')

  const message = (err: unknown, fallback: string) => (err instanceof Error ? err.message : fallback)

  useEffect(() => {
    if (!accessToken || !canRead) return
    listInstallationSenderRules()
      .then(setRules)
      .catch((err: unknown) => setError(message(err, "Could not load the installation's sender rules")))
  }, [accessToken, canRead])

  const handleAdd = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (pattern.trim() === '') {
      setError('Enter an address (spam@example.com) or a domain (@example.com).')
      return
    }
    try {
      const rule = await addInstallationSenderRule({
        pattern: pattern.trim(),
        list_type: listType,
        ...(reason.trim() ? { reason: reason.trim() } : {}),
      })
      setRules([rule, ...rules])
      setPattern('')
      setReason('')
    } catch (err) {
      setError(message(err, 'Could not add the sender rule'))
    }
  }

  const handleRemove = async (id: number) => {
    setError('')
    try {
      await removeInstallationSenderRule(id)
      setRules(rules.filter((rule) => rule.id !== id))
    } catch (err) {
      setError(message(err, 'Could not remove the sender rule'))
    }
  }

  return (
    <AppShell title="Installation sender rules">
      <div className="flex flex-col gap-6 max-w-3xl">
        <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          Every domain inherits these rules. A block wins over an allow, whichever sets them: a domain
          cannot allow a sender blocked here, and each domain can still add blocks of its own.
        </p>
        {!canRead && (
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Viewing these rules needs domains:read for the whole installation.
          </p>
        )}
        {error && <p className="text-sm" style={{ color: '#B91C1C' }}>{error}</p>}

        {canWrite && (
          <form onSubmit={handleAdd} className="flex flex-wrap items-end gap-3">
            <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
              Sender address or @domain
              <input
                className="h-10 px-3 border text-sm rounded"
                style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                placeholder="@spam.example"
                value={pattern}
                onChange={(e) => setPattern(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
              Rule
              <select
                className="h-10 px-3 border text-sm rounded"
                style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                value={listType}
                onChange={(e) => setListType(e.target.value as SenderListType)}
              >
                <option value="block">Block</option>
                <option value="allow">Allow</option>
              </select>
            </label>
            <label className="flex flex-col gap-1 text-xs flex-1" style={{ color: 'var(--gray-secondary)' }}>
              Reason (optional)
              <input
                className="h-10 px-3 border text-sm rounded"
                style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                value={reason}
                onChange={(e) => setReason(e.target.value)}
              />
            </label>
            <button
              type="submit"
              className="h-10 px-5 text-white text-sm font-medium rounded"
              style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}
            >
              Add
            </button>
          </form>
        )}

        {canRead && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs" style={{ color: 'var(--gray-secondary)' }}>
                <th className="py-2">Sender</th>
                <th className="py-2">Rule</th>
                <th className="py-2">Reason</th>
                {canWrite && <th className="py-2" />}
              </tr>
            </thead>
            <tbody>
              {rules.length === 0 && (
                <tr>
                  <td colSpan={canWrite ? 4 : 3} className="py-4" style={{ color: 'var(--gray-secondary)' }}>
                    No installation sender rules.
                  </td>
                </tr>
              )}
              {rules.map((rule) => (
                <tr key={rule.id} className="border-t" style={{ borderColor: 'var(--gray-border)' }}>
                  <td className="py-2" style={{ color: 'var(--black-soft)' }}>{rule.pattern}</td>
                  <td className="py-2">{rule.list_type === 'block' ? 'Block' : 'Allow'}</td>
                  <td className="py-2" style={{ color: 'var(--gray-secondary)' }}>{rule.reason ?? ''}</td>
                  {canWrite && (
                    <td className="py-2 text-right">
                      <button
                        type="button"
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--red-primary)' }}
                        onClick={() => handleRemove(rule.id)}
                      >
                        Remove
                      </button>
                    </td>
                  )}
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </AppShell>
  )
}
