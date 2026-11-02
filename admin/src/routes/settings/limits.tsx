import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { AppShell } from '../../components/layout/AppShell'
import { bytesToMiB, getServerLimits, miBToBytes, updateServerLimits } from '../../lib/serverLimits'
import { useAuthStore } from '../../lib/stores/authStore'

export const Route = createFileRoute('/settings/limits')({
  component: LimitsPage,
})

function LimitsPage() {
  const { accessToken } = useAuthStore()
  const [size, setSize] = useState('')
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    if (!accessToken) return
    getServerLimits()
      .then((limits) => setSize(String(bytesToMiB(limits.contact_import_max_bytes))))
      .catch((err: unknown) => setError(err instanceof Error ? err.message : 'Could not load the server limits'))
  }, [accessToken])

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setSaved(false)
    const bytes = miBToBytes(size)
    if (bytes === null) {
      setError('Enter a size above zero, in MiB.')
      return
    }
    try {
      const limits = await updateServerLimits({ contact_import_max_bytes: bytes })
      setSize(String(bytesToMiB(limits.contact_import_max_bytes)))
      setSaved(true)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not change the server limits')
    }
  }

  return (
    <AppShell title="Server limits">
      <form onSubmit={handleSave} className="flex flex-col gap-4 max-w-md">
        <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
          Changing these needs a superadmin signed in with two-factor authentication.
        </p>
        {error && <p className="text-sm" style={{ color: '#B91C1C' }}>{error}</p>}
        {saved && <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>Saved.</p>}
        <label className="flex flex-col gap-1 text-xs" style={{ color: 'var(--gray-secondary)' }}>
          Largest contact import file, in MiB
          <input
            inputMode="decimal"
            className="h-10 px-3 border text-sm rounded"
            style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
            value={size}
            onChange={(e) => setSize(e.target.value)}
          />
        </label>
        <button type="submit" className="h-10 px-5 self-start text-white text-sm font-medium rounded" style={{ backgroundColor: 'var(--red-primary)', fontFamily: 'Space Grotesk' }}>
          Save
        </button>
      </form>
    </AppShell>
  )
}
