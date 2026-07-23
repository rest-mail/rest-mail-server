import { describe, it, expect } from 'vitest'
import {
  BUILTIN_FILTERS,
  getFiltersByDirection,
  getFiltersByType,
  getFiltersByCategory,
  getFilterDefinition,
  getFiltersByDirectionAndType,
} from './filterRegistryStore'

const names = (list: { name: string }[]) => list.map((f) => f.name)

describe('getFiltersByDirection', () => {
  it('returns only inbound-or-both filters, and never an outbound-only one', () => {
    const inbound = getFiltersByDirection('inbound')
    expect(inbound.length).toBeGreaterThan(0)
    expect(inbound.every((f) => f.direction === 'inbound' || f.direction === 'both')).toBe(true)
    expect(names(inbound)).toContain('spf_check')
    expect(names(inbound)).not.toContain('dkim_sign') // outbound-only
  })

  it('returns only outbound-or-both filters for outbound', () => {
    const outbound = getFiltersByDirection('outbound')
    expect(outbound.every((f) => f.direction === 'outbound' || f.direction === 'both')).toBe(true)
    expect(names(outbound)).toContain('dkim_sign')
    expect(names(outbound)).not.toContain('size_check') // inbound-only
  })
})

describe('getFiltersByType', () => {
  it('partitions filters by type', () => {
    const actions = getFiltersByType('action')
    const transforms = getFiltersByType('transform')
    expect(actions.every((f) => f.type === 'action')).toBe(true)
    expect(transforms.every((f) => f.type === 'transform')).toBe(true)
    expect(names(actions)).toContain('size_check')
    expect(names(transforms)).toContain('dkim_sign')
    // Every builtin lands in exactly one of the two partitions.
    expect(actions.length + transforms.length).toBe(BUILTIN_FILTERS.length)
  })
})

describe('getFiltersByCategory', () => {
  it('returns only filters in the requested category', () => {
    const auth = getFiltersByCategory('authentication')
    expect(auth.length).toBeGreaterThan(0)
    expect(auth.every((f) => f.category === 'authentication')).toBe(true)
    expect(names(auth)).toContain('dkim_sign')
  })

  it('returns an empty array for a category with no members', () => {
    // 'custom' is a valid category with no built-in members.
    expect(getFiltersByCategory('custom')).toEqual([])
  })
})

describe('getFilterDefinition', () => {
  it('looks a filter up by name', () => {
    const def = getFilterDefinition('dkim_sign')
    expect(def).toBeDefined()
    expect(def?.name).toBe('dkim_sign')
    expect(def?.type).toBe('transform')
    expect(def?.direction).toBe('outbound')
  })

  it('returns undefined for an unknown name', () => {
    expect(getFilterDefinition('does_not_exist')).toBeUndefined()
  })
})

describe('getFiltersByDirectionAndType', () => {
  it('intersects direction and type predicates', () => {
    const outboundTransforms = getFiltersByDirectionAndType('outbound', 'transform')
    expect(outboundTransforms.length).toBeGreaterThan(0)
    expect(
      outboundTransforms.every(
        (f) => (f.direction === 'outbound' || f.direction === 'both') && f.type === 'transform'
      )
    ).toBe(true)
    expect(names(outboundTransforms)).toContain('dkim_sign')
  })
})
