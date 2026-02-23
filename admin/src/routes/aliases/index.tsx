import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState, useMemo } from 'react'
import { useAuthStore } from '../../lib/stores/authStore'
import { useAliasStore } from '../../lib/stores/aliasStore'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/aliases/')({
  component: AliasesPage,
})

function AliasesPage() {
  const { accessToken } = useAuthStore()
  const { aliases, isLoading, error, fetchAliases, selectedDomain, setSelectedDomain, deleteAlias } =
    useAliasStore()
  const [searchQuery, setSearchQuery] = useState('')
  const [deletingId, setDeletingId] = useState<number | null>(null)

  useEffect(() => {
    if (accessToken) {
      fetchAliases(accessToken)
    }
  }, [accessToken, fetchAliases])

  // Extract unique domains
  const domains = useMemo(() => {
    const uniqueDomains = Array.from(
      new Set(aliases.map((a) => a.domain?.name).filter(Boolean))
    ) as string[]
    return uniqueDomains.sort()
  }, [aliases])

  // Filter aliases
  const filteredAliases = useMemo(() => {
    return aliases.filter((alias) => {
      const matchesSearch =
        searchQuery === '' ||
        alias.source_address.toLowerCase().includes(searchQuery.toLowerCase()) ||
        alias.destination_address.toLowerCase().includes(searchQuery.toLowerCase())

      const matchesDomain = !selectedDomain || alias.domain?.name === selectedDomain

      return matchesSearch && matchesDomain
    })
  }, [aliases, searchQuery, selectedDomain])

  const handleDelete = async (id: number, sourceAddress: string) => {
    if (!accessToken) return
    if (!confirm(`Are you sure you want to delete alias "${sourceAddress}"?`)) return

    setDeletingId(id)
    try {
      await deleteAlias(accessToken, id)
    } catch (err) {
      console.error('Failed to delete alias:', err)
    } finally {
      setDeletingId(null)
    }
  }

  return (
    <AppShell title="Aliases">
      <div>
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Manage email forwarding and aliases
            </p>
          </div>

          <Link
            to="/aliases/new"
            className="h-11 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
            style={{
              backgroundColor: 'var(--red-primary)',
              fontFamily: 'Space Grotesk',
            }}
          >
            Create Alias
          </Link>
        </div>

        {/* Error Message */}
        {error && (
          <div
            className="p-4 mb-6 border text-sm"
            style={{
              borderColor: '#EF4444',
              backgroundColor: '#FEF2F2',
              color: '#DC2626',
            }}
          >
            {error}
          </div>
        )}

        {/* Filters */}
        <div className="flex gap-4 mb-6">
          {/* Search */}
          <div className="flex-1">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search aliases..."
                className="w-full outline-none text-sm"
                style={{ color: 'var(--black-soft)' }}
              />
            </div>
          </div>

          {/* Domain Filter */}
          <div className="w-64">
            <div
              className="h-11 px-4 flex items-center border"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              <select
                value={selectedDomain || ''}
                onChange={(e) => setSelectedDomain(e.target.value || null)}
                className="w-full outline-none text-sm bg-transparent"
                style={{ color: 'var(--black-soft)' }}
              >
                <option value="">All Domains</option>
                {domains.map((domain) => (
                  <option key={domain} value={domain}>
                    {domain}
                  </option>
                ))}
              </select>
            </div>
          </div>
        </div>

        {/* Table */}
        {isLoading ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              Loading aliases...
            </div>
          </div>
        ) : filteredAliases.length === 0 ? (
          <div className="flex items-center justify-center py-20">
            <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
              {searchQuery || selectedDomain ? 'No aliases found' : 'No aliases yet'}
            </div>
          </div>
        ) : (
          <div className="border" style={{ borderColor: 'var(--gray-border)' }}>
            <table className="w-full">
              <thead>
                <tr
                  className="border-b text-left text-xs font-medium"
                  style={{
                    backgroundColor: 'var(--bg-surface)',
                    borderColor: 'var(--gray-border)',
                    color: 'var(--gray-secondary)',
                  }}
                >
                  <th className="px-6 py-4">Source Address</th>
                  <th className="px-6 py-4">Destination Address</th>
                  <th className="px-6 py-4">Domain</th>
                  <th className="px-6 py-4">Status</th>
                  <th className="px-6 py-4 text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {filteredAliases.map((alias) => (
                  <tr
                    key={alias.id}
                    className="border-b transition-colors hover:bg-gray-50"
                    style={{ borderColor: 'var(--gray-border)' }}
                  >
                    <td className="px-6 py-4">
                      <Link
                        to="/aliases/$id"
                        params={{ id: alias.id.toString() }}
                        className="text-sm font-medium hover:underline"
                        style={{ color: 'var(--black-soft)' }}
                      >
                        {alias.source_address}
                      </Link>
                    </td>
                    <td className="px-6 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {alias.destination_address}
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                        {alias.domain?.name || '—'}
                      </div>
                    </td>
                    <td className="px-6 py-4">
                      <span
                        className="inline-flex items-center px-2 py-1 text-xs font-medium"
                        style={{
                          backgroundColor: alias.active ? '#DCFCE7' : '#F3F4F6',
                          color: alias.active ? '#166534' : '#6B7280',
                        }}
                      >
                        {alias.active ? 'Active' : 'Inactive'}
                      </span>
                    </td>
                    <td className="px-6 py-4 text-right">
                      <div className="flex items-center justify-end gap-2">
                        <Link
                          to="/aliases/$id"
                          params={{ id: alias.id.toString() }}
                          className="text-sm hover:underline"
                          style={{ color: 'var(--red-primary)' }}
                        >
                          Edit
                        </Link>
                        <button
                          onClick={() => handleDelete(alias.id, alias.source_address)}
                          disabled={deletingId === alias.id}
                          className="text-sm hover:underline disabled:opacity-50"
                          style={{ color: '#DC2626' }}
                        >
                          {deletingId === alias.id ? 'Deleting...' : 'Delete'}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {/* Stats */}
        <div className="mt-6 flex items-center justify-between">
          <div className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            Showing {filteredAliases.length} of {aliases.length} aliases
          </div>
        </div>
      </div>
    </AppShell>
  )
}
