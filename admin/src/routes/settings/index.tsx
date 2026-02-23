import { createFileRoute, Link } from '@tanstack/react-router'
import { AppShell } from '../../components/layout/AppShell'
import { useAuthStore } from '../../lib/stores/authStore'
import { useCertificateStore } from '../../lib/stores/certificateStore'
import { useEffect } from 'react'

export const Route = createFileRoute('/settings/')({
  component: SettingsPage,
})

function SettingsPage() {
  const { accessToken } = useAuthStore()
  const { certificates, getExpiringCertificates, fetchCertificates } = useCertificateStore()

  useEffect(() => {
    if (accessToken) {
      fetchCertificates(accessToken).catch(() => {
        // Silently fail - user may not have permissions
      })
    }
  }, [accessToken])

  const expiringCount = getExpiringCertificates(30).length

  const settingsCards = [
    {
      title: 'DKIM Keys',
      description: 'Manage DKIM signing keys for email authentication',
      icon: '🔑',
      link: '/settings/dkim',
    },
    {
      title: 'Certificates',
      description: 'Upload and manage SSL/TLS certificates',
      icon: '🔒',
      link: '/settings/certificates',
      badge: expiringCount > 0 ? `${expiringCount} expiring soon` : undefined,
    },
    {
      title: 'IP Bans',
      description: 'Manage IP address blocklist for security',
      icon: '🚫',
      link: '/settings/bans',
    },
    {
      title: 'TLS Reports',
      description: 'View TLS reporting from external MTAs',
      icon: '📊',
      link: '/settings/tls-reports',
    },
    {
      title: 'MTA-STS',
      description: 'Configure MTA-STS policy for secure email delivery',
      icon: '📧',
      link: '/settings/mta-sts',
    },
  ]

  return (
    <AppShell title="Settings">
      <div>
        <p className="text-sm mb-6" style={{ color: 'var(--gray-secondary)' }}>
          Configure system settings and preferences
        </p>

        {/* Content */}
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
          {settingsCards.map((card) => (
            <div
              key={card.link}
              className="border p-6 hover:shadow-md transition-shadow relative"
              style={{ borderColor: 'var(--gray-border)' }}
            >
              {/* Icon */}
              <div className="text-4xl mb-4">{card.icon}</div>

              {/* Title */}
              <h2
                className="text-lg font-semibold mb-2"
                style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
              >
                {card.title}
              </h2>

              {/* Description */}
              <p className="text-sm mb-4" style={{ color: 'var(--gray-secondary)' }}>
                {card.description}
              </p>

              {/* Badge */}
              {card.badge && (
                <div
                  className="inline-block px-2 py-1 text-[11px] font-medium mb-3"
                  style={{
                    backgroundColor: '#FEF3C7',
                    color: '#92400E',
                  }}
                >
                  ⚠️ {card.badge}
                </div>
              )}

              {/* Link */}
              <div className="mt-4">
                <Link
                  to={card.link}
                  className="text-sm font-medium hover:underline"
                  style={{ color: 'var(--red-primary)' }}
                >
                  Configure →
                </Link>
              </div>
            </div>
          ))}
        </div>

        {/* Information Card */}
        <div
          className="mt-8 p-6 border"
          style={{
            borderColor: 'var(--gray-border)',
            backgroundColor: 'var(--bg-surface)',
          }}
        >
          <h3
            className="text-base font-semibold mb-2"
            style={{ fontFamily: 'Space Grotesk', color: 'var(--black-soft)' }}
          >
            About Settings
          </h3>
          <p className="text-sm" style={{ color: 'var(--gray-secondary)' }}>
            The settings section provides configuration options for various system components including
            email authentication (DKIM), security (SSL/TLS certificates, IP bans), and email delivery
            policies (MTA-STS, TLS-RPT).
          </p>
        </div>
      </div>
    </AppShell>
  )
}
