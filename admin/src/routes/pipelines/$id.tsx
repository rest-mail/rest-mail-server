import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { usePipelineStore, type FilterConfig } from '../../lib/stores/pipelineStore'
import { useDomainStore } from '../../lib/stores/domainStore'
import { useAuthStore } from '../../lib/stores/authStore'
import { useUIStore } from '../../lib/stores/uiStore'
import { AppShell } from '../../components/layout/AppShell'
import { getFiltersByDirection, getFilterDefinition } from '../../lib/stores/filterRegistryStore'
import { X, GripVertical, Lock, Plus } from 'lucide-react'

export const Route = createFileRoute('/pipelines/$id')({
  component: PipelineEditorPage,
})

function PipelineEditorPage() {
  const { id } = Route.useParams()
  const navigate = useNavigate()
  const { currentPipeline, fetchPipeline, updatePipeline, isLoading, error, clearError } = usePipelineStore()
  const { domains, fetchDomains } = useDomainStore()
  const { accessToken, isAuthenticated } = useAuthStore()
  const { addNotification } = useUIStore()
  const [domainId, setDomainId] = useState<number>(0)
  const [direction, setDirection] = useState<'inbound' | 'outbound'>('inbound')
  const [active, setActive] = useState(true)
  const [filters, setFilters] = useState<FilterConfig[]>([])
  const [selectedFilterIndex, setSelectedFilterIndex] = useState<number | null>(null)
  const [showAddFilter, setShowAddFilter] = useState(false)

  useEffect(() => {
    if (!isAuthenticated) {
      navigate({ to: '/login' })
      return
    }

    if (accessToken) {
      fetchDomains(accessToken).catch((err) => {
        console.error('Failed to fetch domains:', err)
      })
      fetchPipeline(parseInt(id), accessToken).catch((err) => {
        console.error('Failed to fetch pipeline:', err)
      })
    }
  }, [isAuthenticated, accessToken, id, navigate, fetchPipeline, fetchDomains])

  useEffect(() => {
    if (currentPipeline) {
      setDomainId(currentPipeline.domain_id)
      setDirection(currentPipeline.direction)
      setActive(currentPipeline.active)
      setFilters(currentPipeline.filters || [])
    }
  }, [currentPipeline])

  const handleSave = async () => {
    if (!accessToken) return

    try {
      await updatePipeline(
        parseInt(id),
        {
          domain_id: domainId,
          direction,
          active,
          filters,
        },
        accessToken
      )
      addNotification({
        type: 'success',
        message: 'Pipeline updated successfully',
      })
      navigate({ to: '/pipelines' })
    } catch (err) {
      console.error('Failed to update pipeline:', err)
      addNotification({
        type: 'error',
        message: 'Failed to update pipeline',
      })
    }
  }

  const handleAddFilter = (filterName: string) => {
    const definition = getFilterDefinition(filterName)
    if (!definition) return

    const defaultConfig: Record<string, any> = {}
    if (definition.configSchema) {
      Object.entries(definition.configSchema).forEach(([key, field]) => {
        if (field.default !== undefined) {
          defaultConfig[key] = field.default
        }
      })
    }

    const newFilter: FilterConfig = {
      name: filterName,
      type: definition.type,
      enabled: true,
      unskippable: definition.unskippable,
      config: defaultConfig,
    }

    setFilters([...filters, newFilter])
    setShowAddFilter(false)
  }

  const handleRemoveFilter = (index: number) => {
    const filter = filters[index]
    if (filter.unskippable) {
      addNotification({
        type: 'warning',
        message: 'Cannot remove unskippable filter',
      })
      return
    }
    setFilters(filters.filter((_, i) => i !== index))
    if (selectedFilterIndex === index) {
      setSelectedFilterIndex(null)
    }
  }

  const handleToggleFilter = (index: number) => {
    const filter = filters[index]
    if (filter.unskippable && filter.enabled) {
      addNotification({
        type: 'warning',
        message: 'Cannot disable unskippable filter',
      })
      return
    }
    setFilters(
      filters.map((f, i) => (i === index ? { ...f, enabled: !f.enabled } : f))
    )
  }

  const handleUpdateFilterConfig = (index: number, config: Record<string, any>) => {
    setFilters(
      filters.map((f, i) => (i === index ? { ...f, config } : f))
    )
  }

  const handleMoveFilter = (index: number, direction: 'up' | 'down') => {
    const newFilters = [...filters]
    const newIndex = direction === 'up' ? index - 1 : index + 1
    if (newIndex < 0 || newIndex >= newFilters.length) return

    [newFilters[index], newFilters[newIndex]] = [newFilters[newIndex], newFilters[index]]
    setFilters(newFilters)
    if (selectedFilterIndex === index) {
      setSelectedFilterIndex(newIndex)
    }
  }

  const availableFilters = getFiltersByDirection(direction)

  return (
    <AppShell title="Edit Pipeline">
      <div className="mb-6">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-2xl font-bold" style={{ color: 'var(--black-soft)' }}>
            Edit Pipeline
          </h2>
          <div className="flex gap-3">
            <button
              onClick={() => navigate({ to: '/pipelines' })}
              className="h-10 px-6 flex items-center justify-center text-sm font-medium rounded border"
              style={{
                borderColor: 'var(--gray-border)',
                color: 'var(--gray-secondary)',
                fontFamily: 'Space Grotesk',
              }}
            >
              Cancel
            </button>
            <button
              onClick={handleSave}
              disabled={isLoading || domainId === 0}
              className="h-10 px-6 flex items-center justify-center text-white text-sm font-medium rounded"
              style={{
                backgroundColor: 'var(--red-primary)',
                fontFamily: 'Space Grotesk',
                opacity: isLoading || domainId === 0 ? 0.5 : 1,
              }}
            >
              {isLoading ? 'Saving...' : 'Save Pipeline'}
            </button>
          </div>
        </div>

        {/* Error Message */}
        {error && (
          <div className="mb-4">
            <div
              className="p-4 border flex items-center justify-between rounded"
              style={{
                borderColor: '#EF4444',
                backgroundColor: '#FEF2F2',
                color: '#DC2626',
              }}
            >
              <span className="text-sm">{error}</span>
              <button
                onClick={clearError}
                className="text-sm font-medium"
                style={{ color: '#DC2626' }}
              >
                Dismiss
              </button>
            </div>
          </div>
        )}
      </div>

      <div className="grid grid-cols-3 gap-6">
        {/* Left Panel - Configuration */}
        <div className="col-span-1 space-y-6">
          <div className="border rounded p-6" style={{ borderColor: 'var(--gray-border)' }}>
            <h3 className="text-lg font-semibold mb-4" style={{ color: 'var(--black-soft)' }}>
              Configuration
            </h3>

            <div className="space-y-4">
              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Domain
                </label>
                <select
                  value={domainId}
                  onChange={(e) => setDomainId(parseInt(e.target.value))}
                  className="w-full h-11 px-4 border rounded text-sm"
                  style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                >
                  <option value={0}>Select domain...</option>
                  {domains.map((domain) => (
                    <option key={domain.id} value={domain.id}>
                      {domain.name}
                    </option>
                  ))}
                </select>
              </div>

              <div>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--black-soft)' }}>
                  Direction
                </label>
                <div className="flex gap-2">
                  <button
                    onClick={() => setDirection('inbound')}
                    className="flex-1 h-10 text-sm font-medium border rounded"
                    style={{
                      borderColor: direction === 'inbound' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: direction === 'inbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: direction === 'inbound' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Inbound
                  </button>
                  <button
                    onClick={() => setDirection('outbound')}
                    className="flex-1 h-10 text-sm font-medium border rounded"
                    style={{
                      borderColor: direction === 'outbound' ? 'var(--red-primary)' : 'var(--gray-border)',
                      color: direction === 'outbound' ? 'var(--red-primary)' : 'var(--gray-secondary)',
                      backgroundColor: direction === 'outbound' ? '#FEF2F2' : 'white',
                    }}
                  >
                    Outbound
                  </button>
                </div>
              </div>

              <div>
                <label className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={active}
                    onChange={(e) => setActive(e.target.checked)}
                    className="w-4 h-4"
                  />
                  <span className="text-sm font-medium" style={{ color: 'var(--black-soft)' }}>
                    Active
                  </span>
                </label>
              </div>
            </div>
          </div>
        </div>

        {/* Middle Panel - Filter List */}
        <div className="col-span-2">
          <div className="border rounded" style={{ borderColor: 'var(--gray-border)' }}>
            <div className="p-6 border-b" style={{ borderColor: 'var(--gray-border)' }}>
              <div className="flex items-center justify-between">
                <h3 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
                  Filters ({filters.length})
                </h3>
                <button
                  onClick={() => setShowAddFilter(true)}
                  className="h-10 px-4 flex items-center gap-2 text-sm font-medium rounded border"
                  style={{
                    borderColor: 'var(--red-primary)',
                    color: 'var(--red-primary)',
                    fontFamily: 'Space Grotesk',
                  }}
                >
                  <Plus className="w-4 h-4" />
                  Add Filter
                </button>
              </div>
            </div>

            <div className="p-6">
              {filters.length === 0 ? (
                <div className="text-center py-12">
                  <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
                    No filters added yet. Click "Add Filter" to get started.
                  </p>
                </div>
              ) : (
                <div className="space-y-3">
                  {filters.map((filter, index) => {
                    const definition = getFilterDefinition(filter.name)
                    return (
                      <div
                        key={index}
                        className="border rounded p-4 cursor-pointer hover:border-gray-400 transition-colors"
                        style={{
                          borderColor: selectedFilterIndex === index ? 'var(--red-primary)' : 'var(--gray-border)',
                          backgroundColor: selectedFilterIndex === index ? '#FEF2F2' : 'white',
                        }}
                        onClick={() => setSelectedFilterIndex(index)}
                      >
                        <div className="flex items-center gap-3">
                          <div className="flex flex-col gap-1">
                            <button
                              onClick={(e) => {
                                e.stopPropagation()
                                handleMoveFilter(index, 'up')
                              }}
                              disabled={index === 0}
                              className="text-gray-400 hover:text-gray-600 disabled:opacity-30"
                            >
                              ▲
                            </button>
                            <button
                              onClick={(e) => {
                                e.stopPropagation()
                                handleMoveFilter(index, 'down')
                              }}
                              disabled={index === filters.length - 1}
                              className="text-gray-400 hover:text-gray-600 disabled:opacity-30"
                            >
                              ▼
                            </button>
                          </div>

                          <div className="flex-1">
                            <div className="flex items-center gap-2 mb-1">
                              <span className="text-sm font-semibold" style={{ color: 'var(--black-soft)' }}>
                                {definition?.displayName || filter.name}
                              </span>
                              {filter.unskippable && (
                                <Lock className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                              )}
                              <span
                                className="text-xs px-2 py-0.5 rounded"
                                style={{
                                  backgroundColor: filter.type === 'action' ? '#DBEAFE' : '#FEF3C7',
                                  color: filter.type === 'action' ? '#1E40AF' : '#92400E',
                                }}
                              >
                                {filter.type}
                              </span>
                            </div>
                            {definition?.description && (
                              <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                                {definition.description}
                              </p>
                            )}
                          </div>

                          <div className="flex items-center gap-2">
                            <label className="flex items-center gap-2">
                              <input
                                type="checkbox"
                                checked={filter.enabled}
                                onChange={(e) => {
                                  e.stopPropagation()
                                  handleToggleFilter(index)
                                }}
                                className="w-4 h-4"
                              />
                              <span className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                                Enabled
                              </span>
                            </label>
                            {!filter.unskippable && (
                              <button
                                onClick={(e) => {
                                  e.stopPropagation()
                                  handleRemoveFilter(index)
                                }}
                                className="text-gray-400 hover:text-red-600"
                              >
                                <X className="w-5 h-5" />
                              </button>
                            )}
                          </div>
                        </div>

                        {selectedFilterIndex === index && definition?.configSchema && (
                          <div className="mt-4 pt-4 border-t" style={{ borderColor: 'var(--gray-border)' }}>
                            <h4 className="text-sm font-semibold mb-3" style={{ color: 'var(--black-soft)' }}>
                              Configuration
                            </h4>
                            <div className="space-y-3">
                              {Object.entries(definition.configSchema).map(([key, field]) => (
                                <div key={key}>
                                  <label className="block text-xs font-medium mb-1" style={{ color: 'var(--black-soft)' }}>
                                    {field.label}
                                  </label>
                                  {field.type === 'select' ? (
                                    <select
                                      value={filter.config?.[key] || field.default}
                                      onChange={(e) => {
                                        const newConfig = { ...filter.config, [key]: e.target.value }
                                        handleUpdateFilterConfig(index, newConfig)
                                      }}
                                      className="w-full h-9 px-3 border rounded text-xs"
                                      style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                                    >
                                      {field.options?.map((option) => (
                                        <option key={option} value={option}>
                                          {option}
                                        </option>
                                      ))}
                                    </select>
                                  ) : field.type === 'number' ? (
                                    <input
                                      type="number"
                                      value={filter.config?.[key] || field.default}
                                      onChange={(e) => {
                                        const newConfig = { ...filter.config, [key]: parseInt(e.target.value) }
                                        handleUpdateFilterConfig(index, newConfig)
                                      }}
                                      min={field.min}
                                      max={field.max}
                                      className="w-full h-9 px-3 border rounded text-xs"
                                      style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                                    />
                                  ) : (
                                    <input
                                      type="text"
                                      value={filter.config?.[key] || field.default}
                                      onChange={(e) => {
                                        const newConfig = { ...filter.config, [key]: e.target.value }
                                        handleUpdateFilterConfig(index, newConfig)
                                      }}
                                      className="w-full h-9 px-3 border rounded text-xs"
                                      style={{ borderColor: 'var(--gray-border)', color: 'var(--black-soft)' }}
                                    />
                                  )}
                                  {field.description && (
                                    <p className="text-xs mt-1" style={{ color: 'var(--gray-secondary)' }}>
                                      {field.description}
                                    </p>
                                  )}
                                </div>
                              ))}
                            </div>
                          </div>
                        )}
                      </div>
                    )
                  })}
                </div>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Add Filter Modal */}
      {showAddFilter && (
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50">
          <div className="bg-white rounded-lg p-6 max-w-2xl w-full max-h-[80vh] overflow-y-auto">
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-lg font-semibold" style={{ color: 'var(--black-soft)' }}>
                Add Filter
              </h3>
              <button onClick={() => setShowAddFilter(false)}>
                <X className="w-6 h-6" style={{ color: 'var(--gray-secondary)' }} />
              </button>
            </div>

            <div className="space-y-2">
              {availableFilters.map((filter) => (
                <button
                  key={filter.name}
                  onClick={() => handleAddFilter(filter.name)}
                  className="w-full text-left p-4 border rounded hover:border-gray-400 transition-colors"
                  style={{ borderColor: 'var(--gray-border)' }}
                >
                  <div className="flex items-center justify-between">
                    <div className="flex-1">
                      <div className="flex items-center gap-2 mb-1">
                        <span className="text-sm font-semibold" style={{ color: 'var(--black-soft)' }}>
                          {filter.displayName}
                        </span>
                        {filter.unskippable && (
                          <Lock className="w-4 h-4" style={{ color: 'var(--gray-secondary)' }} />
                        )}
                      </div>
                      <p className="text-xs" style={{ color: 'var(--gray-secondary)' }}>
                        {filter.description}
                      </p>
                    </div>
                    <span
                      className="text-xs px-2 py-1 rounded"
                      style={{
                        backgroundColor: filter.type === 'action' ? '#DBEAFE' : '#FEF3C7',
                        color: filter.type === 'action' ? '#1E40AF' : '#92400E',
                      }}
                    >
                      {filter.type}
                    </span>
                  </div>
                </button>
              ))}
            </div>
          </div>
        </div>
      )}
    </AppShell>
  )
}
