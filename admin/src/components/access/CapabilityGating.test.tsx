import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import type { ComponentType, ReactNode } from 'react'
import { Sidebar } from '../layout/Sidebar'
import { ScreenGate } from './ScreenGate'
import { Route as DashboardRoute } from '../../routes/dashboard/index'
import { useAuthStore } from '../../lib/stores/authStore'
import { useUIStore } from '../../lib/stores/uiStore'
import { canReach, needOf } from '../../lib/access'

// These tests sit outside src/routes because the router plugin takes every file there as a route.
const router = vi.hoisted(() => ({ pathname: '/' }))
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(),
  useRouterState: (opts?: { select?: (s: unknown) => unknown }) => {
    const state = { location: { pathname: router.pathname } }
    return opts?.select ? opts.select(state) : state
  },
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
}))
vi.mock('../layout/AppShell', () => ({
  AppShell: ({ children }: { children: ReactNode }) => <>{children}</>,
}))

// The seeded admin role, granted at one domain (#1709): no capability at "*".
const domainAdmin: User = {
  email: 'owner@customer.test',
  capabilities: [],
  scopedCapabilities: [
    'domains:read', 'domains:write', 'domains:delete',
    'accounts:read', 'accounts:write', 'accounts:delete',
    'pipelines:read', 'pipelines:write', 'pipelines:delete',
    'users:read', 'messages:read', 'queue:read', 'queue:manage',
    'bans:read', 'observability:read', 'audit:read',
  ].map((capability) => ({ capability, scope: 'domain:customer.test' })),
}
const superadmin: User = { email: 'root@example.test', capabilities: ['*'], scopedCapabilities: [] }

type User = NonNullable<ReturnType<typeof useAuthStore.getState>['user']>

function signIn(user: User) {
  useAuthStore.setState({ user, accessToken: 'cookie-session', isAuthenticated: true, sessionChecked: true })
}

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status })
}

beforeEach(() => {
  useUIStore.setState({ sidebarOpen: true })
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  router.pathname = '/'
})

describe('what each screen needs', () => {
  it('follows the route registration: whole-server routes need the capability at "*"', () => {
    expect(needOf('/queue/12')).toEqual({ capability: 'queue:read', at: 'server' })
    expect(needOf('/reputation')).toEqual({ capability: 'queue:read', at: 'server' })
    expect(needOf('/settings/dkim')).toEqual({ capability: 'domains:read', at: 'server' })
    expect(needOf('/domains/7')).toEqual({ capability: 'domains:read', at: 'anywhere' })
    expect(needOf('/domains/new')).toEqual({ capability: 'domains:write', at: 'server' })
    expect(needOf('/pipelines/logs')).toEqual({ capability: 'pipelines:read', at: 'server' })
    expect(needOf('/dashboard')).toBeNull()
    expect(needOf('/settings/two-factor')).toBeNull()
    expect(canReach(domainAdmin, '/domains/7')).toBe(true)
    expect(canReach(domainAdmin, '/queue')).toBe(false)
    expect(canReach(domainAdmin, '/reputation')).toBe(false)
    expect(canReach({ capabilities: ['queue:read'] }, '/reputation')).toBe(true)
    expect(canReach(superadmin, '/queue')).toBe(true)
  })
})

describe('the menu', () => {
  const entries = () => screen.getAllByRole('link').map((link) => link.textContent)

  it('shows a domain admin only the screens its grants reach', () => {
    signIn(domainAdmin)
    render(<Sidebar />)
    expect(entries()).toEqual([
      'Dashboard', 'Domains', 'Mailboxes', 'Aliases', 'Pipelines', 'Custom Filters', 'API keys', 'Settings',
    ])
  })

  it('shows whole-server access every screen', () => {
    signIn(superadmin)
    render(<Sidebar />)
    expect(entries()).toContain('Queue')
    expect(entries()).toContain('Reputation')
    expect(entries()).toContain('Grants')
  })
})

describe('a screen reached directly', () => {
  it.each(['/settings/dkim', '/reputation'])('denies a domain admin direct access to %s without mounting the screen', (pathname) => {
    signIn(domainAdmin)
    router.pathname = pathname
    const screenBody = vi.fn(() => <p>DKIM keys</p>)
    const Screen = screenBody as unknown as ComponentType
    render(<ScreenGate><Screen /></ScreenGate>)
    expect(screen.getByText("You don't have access to this")).toBeTruthy()
    expect(screen.getByText(/ask whoever runs the server/)).toBeTruthy()
    expect(screenBody).not.toHaveBeenCalled()
  })

  it('shows the screen to a session holding what it needs', () => {
    signIn(domainAdmin)
    router.pathname = '/domains/7'
    render(<ScreenGate><p>Domain page</p></ScreenGate>)
    expect(screen.getByText('Domain page')).toBeTruthy()
  })
})

describe('the dashboard', () => {
  it("shows a domain admin its domains, DNS and report counts, and never the whole-server stats", async () => {
    signIn(domainAdmin)
    const fetcher = vi.fn(async (input: string) => {
      const url = String(input)
      if (url.endsWith('/admin/domains')) {
        return json(200, { data: [{ id: 7, name: 'customer.test', active: true }] })
      }
      if (url.includes('/admin/domains/7/dns')) {
        return json(200, { data: { domain: 'customer.test', summary: '', records: [
          { name: 'customer.test', type: 'MX', status: 'ok' },
          { name: 'customer.test', type: 'TXT', status: 'missing' },
        ] } })
      }
      if (url.includes('/admin/dmarc-reports')) return json(200, { data: [], pagination: { total: 3, has_more: true } })
      if (url.includes('/admin/tls-reports')) return json(200, { data: [], pagination: { total: 5, has_more: true } })
      return json(403, { error: { code: 'forbidden', message: 'Insufficient permissions' } })
    })
    vi.stubGlobal('fetch', fetcher)
    const Page = DashboardRoute.options.component as ComponentType
    render(<Page />)

    expect(await screen.findByText('customer.test')).toBeTruthy()
    expect(await screen.findByText(/1 of 2 DNS records need attention/)).toBeTruthy()
    await waitFor(() => expect(screen.getByText('3')).toBeTruthy())
    expect(screen.getByText('5')).toBeTruthy()
    expect(screen.queryByText('Failed to load dashboard')).toBeNull()
    expect(screen.queryByText('Insufficient permissions')).toBeNull()
    const called = fetcher.mock.calls.map(([url]) => String(url))
    expect(called.some((url) => url.includes('/admin/stats'))).toBe(false)
    expect(called.some((url) => url.includes('/admin/pipelines/analytics'))).toBe(false)
  })

  it('tells an account with no admin access that it has none', () => {
    signIn({ email: 'user@customer.test', capabilities: [], scopedCapabilities: [] })
    vi.stubGlobal('fetch', vi.fn())
    const Page = DashboardRoute.options.component as ComponentType
    render(<Page />)
    expect(screen.getByText("You don't have access to this")).toBeTruthy()
  })
})
