import { createFileRoute } from '@tanstack/react-router'
import { useEffect } from 'react'
import { BarChart, Bar, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from 'recharts'
import { useAuthStore } from '../../lib/stores/authStore'
import { useDashboardStore } from '../../lib/stores/dashboardStore'
import { Server, Mail, Clock, AlertCircle, Activity, RefreshCw } from 'lucide-react'
import { AppShell } from '../../components/layout/AppShell'

export const Route = createFileRoute('/dashboard/')({
  component: Dashboard,
})

function Dashboard() {
  const { accessToken } = useAuthStore()
  const { stats, isLoading, error, fetchDashboardStats } = useDashboardStore()

  useEffect(() => {
    if (!accessToken) return

    // Initial fetch
    fetchDashboardStats(accessToken)

    // Set up interval for auto-refresh (30 seconds)
    const interval = setInterval(() => {
      if (document.visibilityState === 'visible') {
        fetchDashboardStats(accessToken)
      }
    }, 30000)

    // Cleanup interval on unmount
    return () => clearInterval(interval)
  }, [accessToken, fetchDashboardStats])

  // Listen for visibility changes to refresh when tab becomes visible
  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible' && accessToken) {
        fetchDashboardStats(accessToken)
      }
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    return () => document.removeEventListener('visibilitychange', handleVisibilityChange)
  }, [accessToken, fetchDashboardStats])

  if (isLoading && !stats) {
    return (
      <AppShell title="Dashboard">
        <div className="flex items-center justify-center h-96">
          <div className="text-center">
            <div className="w-8 h-8 border-4 border-gray-200 border-t-[var(--red-primary)] rounded-full animate-spin mx-auto mb-4"></div>
            <p style={{ color: 'var(--gray-secondary)' }}>Loading dashboard...</p>
          </div>
        </div>
      </AppShell>
    )
  }

  if (error) {
    return (
      <AppShell title="Dashboard">
        <div className="flex items-center justify-center h-96">
          <div className="text-center">
            <AlertCircle className="w-12 h-12 mx-auto mb-4" style={{ color: 'var(--red-primary)' }} />
            <p style={{ color: 'var(--black-soft)' }} className="font-semibold mb-2">
              Failed to load dashboard
            </p>
            <p style={{ color: 'var(--gray-secondary)' }}>{error}</p>
          </div>
        </div>
      </AppShell>
    )
  }

  return (
    <AppShell title="Dashboard">
      <div>
        {/* Dashboard Header with Refresh Button */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <h1
              style={{
                fontFamily: 'Space Grotesk, sans-serif',
                color: 'var(--black-soft)',
              }}
              className="text-2xl font-bold"
            >
              Dashboard
            </h1>
            <p style={{ color: 'var(--gray-secondary)' }} className="text-sm mt-1">
              Overview of your mail server statistics
            </p>
          </div>
          <button
            onClick={() => accessToken && fetchDashboardStats(accessToken)}
            disabled={isLoading}
            className="flex items-center gap-2 px-4 py-2 rounded-lg"
            style={{
              border: '1px solid var(--gray-border)',
              color: 'var(--black-soft)',
            }}
          >
            <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
            Refresh
          </button>
        </div>

        {/* Metrics Cards */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-6 mb-8">
          {/* Total Domains */}
          <MetricCard
            icon={<Server className="w-6 h-6" />}
            label="Total Domains"
            value={stats?.domainCount ?? 0}
            color="var(--black-soft)"
          />

          {/* Total Mailboxes */}
          <MetricCard
            icon={<Mail className="w-6 h-6" />}
            label="Total Mailboxes"
            value={stats?.mailboxCount ?? 0}
            color="var(--black-soft)"
          />

          {/* Queue Pending */}
          <MetricCard
            icon={<Clock className="w-6 h-6" />}
            label="Queue Pending"
            value={stats?.queueStats.pending ?? 0}
            color="var(--red-primary)"
            isHighlight
          />

          {/* Queue Failed */}
          <MetricCard
            icon={<AlertCircle className="w-6 h-6" />}
            label="Queue Failed"
            value={stats?.queueStats.failed ?? 0}
            color={stats?.queueStats.failed ? 'var(--red-primary)' : 'var(--gray-secondary)'}
            isHighlight={!!stats?.queueStats.failed}
          />
        </div>

        {/* Charts and Activity Grid */}
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-6">
          {/* Message Volume Chart */}
          <div
            className="lg:col-span-2 p-6 rounded-lg"
            style={{ border: '1px solid var(--gray-border)' }}
          >
            <h2
              style={{
                fontFamily: 'Space Grotesk, sans-serif',
                color: 'var(--black-soft)',
              }}
              className="text-xl font-semibold mb-6"
            >
              Message Volume
            </h2>
            <div className="h-80">
              {stats?.messageVolume && stats.messageVolume.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={stats.messageVolume}>
                    <CartesianGrid strokeDasharray="3 3" stroke="var(--gray-border)" />
                    <XAxis
                      dataKey="date"
                      tick={{ fill: 'var(--gray-secondary)', fontSize: 12 }}
                      axisLine={{ stroke: 'var(--gray-border)' }}
                    />
                    <YAxis
                      tick={{ fill: 'var(--gray-secondary)', fontSize: 12 }}
                      axisLine={{ stroke: 'var(--gray-border)' }}
                    />
                    <Tooltip
                      contentStyle={{
                        backgroundColor: 'white',
                        border: '1px solid var(--gray-border)',
                        borderRadius: '8px',
                        fontSize: '14px',
                      }}
                      labelStyle={{ color: 'var(--black-soft)', fontWeight: 600 }}
                    />
                    <Bar dataKey="count" fill="var(--red-primary)" radius={[4, 4, 0, 0]} />
                  </BarChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex items-center justify-center h-full">
                  <div className="text-center">
                    <Activity className="w-12 h-12 mx-auto mb-4" style={{ color: 'var(--gray-secondary)' }} />
                    <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
                      No message volume data available
                    </p>
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* Recent Activity */}
          <div
            className="p-6 rounded-lg"
            style={{ border: '1px solid var(--gray-border)' }}
          >
            <h2
              style={{
                fontFamily: 'Space Grotesk, sans-serif',
                color: 'var(--black-soft)',
              }}
              className="text-xl font-semibold mb-6"
            >
              Recent Activity
            </h2>
            <div className="space-y-4 max-h-80 overflow-y-auto">
              {stats?.recentActivity && stats.recentActivity.length > 0 ? (
                stats.recentActivity.map((activity) => (
                  <ActivityItem key={activity.id} activity={activity} />
                ))
              ) : (
                <p style={{ color: 'var(--gray-secondary)' }} className="text-sm text-center py-8">
                  No recent activity
                </p>
              )}
            </div>
          </div>
        </div>
      </div>
    </AppShell>
  )
}

interface MetricCardProps {
  icon: React.ReactNode
  label: string
  value: number
  color: string
  isHighlight?: boolean
}

function MetricCard({ icon, label, value, color, isHighlight }: MetricCardProps) {
  return (
    <div
      className="p-6 rounded-lg"
      style={{ border: '1px solid var(--gray-border)' }}
    >
      <div className="flex items-start justify-between mb-4">
        <div
          className="p-2 rounded-lg"
          style={{ backgroundColor: isHighlight ? 'rgba(228, 35, 19, 0.1)' : 'var(--bg-surface)' }}
        >
          <div style={{ color }}>{icon}</div>
        </div>
      </div>
      <div>
        <p
          style={{
            fontFamily: 'Space Grotesk, sans-serif',
            color,
          }}
          className="text-3xl font-bold mb-1"
        >
          {value.toLocaleString()}
        </p>
        <p style={{ color: 'var(--gray-secondary)' }} className="text-sm">
          {label}
        </p>
      </div>
    </div>
  )
}

interface ActivityItemProps {
  activity: {
    type: 'domain_created' | 'mailbox_created' | 'message_sent' | 'message_received'
    description: string
    timestamp: string
  }
}

function ActivityItem({ activity }: ActivityItemProps) {
  const getActivityIcon = () => {
    switch (activity.type) {
      case 'domain_created':
        return <Server className="w-4 h-4" />
      case 'mailbox_created':
        return <Mail className="w-4 h-4" />
      case 'message_sent':
      case 'message_received':
        return <Activity className="w-4 h-4" />
      default:
        return <Activity className="w-4 h-4" />
    }
  }

  const formatTimestamp = (timestamp: string) => {
    const date = new Date(timestamp)
    const now = new Date()
    const diffInSeconds = Math.floor((now.getTime() - date.getTime()) / 1000)

    if (diffInSeconds < 60) return 'Just now'
    if (diffInSeconds < 3600) return `${Math.floor(diffInSeconds / 60)}m ago`
    if (diffInSeconds < 86400) return `${Math.floor(diffInSeconds / 3600)}h ago`
    return `${Math.floor(diffInSeconds / 86400)}d ago`
  }

  return (
    <div className="flex items-start gap-3">
      <div
        className="p-2 rounded-lg flex-shrink-0"
        style={{ backgroundColor: 'var(--bg-surface)' }}
      >
        <div style={{ color: 'var(--gray-secondary)' }}>{getActivityIcon()}</div>
      </div>
      <div className="flex-1 min-w-0">
        <p style={{ color: 'var(--black-soft)' }} className="text-sm font-medium mb-1">
          {activity.description}
        </p>
        <p style={{ color: 'var(--gray-secondary)' }} className="text-xs">
          {formatTimestamp(activity.timestamp)}
        </p>
      </div>
    </div>
  )
}
