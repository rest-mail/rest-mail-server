import { Link, useNavigate, useRouterState } from '@tanstack/react-router';
import {
  Home,
  Globe,
  Mail,
  Inbox,
  Users,
  Settings,
  LogOut,
  Forward,
  Filter,
  Code,
} from 'lucide-react';
import { useUIStore } from '#/lib/stores/uiStore';
import { useAuthStore } from '#/lib/stores/authStore';

interface NavItem {
  to: string;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
}

const navItems: NavItem[] = [
  { to: '/', icon: Home, label: 'Dashboard' },
  { to: '/domains', icon: Globe, label: 'Domains' },
  { to: '/mailboxes', icon: Mail, label: 'Mailboxes' },
  { to: '/aliases', icon: Forward, label: 'Aliases' },
  { to: '/queue', icon: Inbox, label: 'Queue' },
  { to: '/pipelines', icon: Filter, label: 'Pipelines' },
  { to: '/custom-filters', icon: Code, label: 'Custom Filters' },
  { to: '/admin-users', icon: Users, label: 'Admin Users' },
  { to: '/settings', icon: Settings, label: 'Settings' },
];

export function Sidebar() {
  const navigate = useNavigate();
  const sidebarOpen = useUIStore((state) => state.sidebarOpen);
  const logout = useAuthStore((state) => state.logout);
  const router = useRouterState();
  const currentPath = router.location.pathname;

  if (!sidebarOpen) {
    return null;
  }

  const handleLogout = () => {
    logout();
    navigate({ to: '/login' });
  };

  return (
    <aside className="w-60 h-screen bg-white border-r border-[var(--gray-border)] flex flex-col">
      {/* Logo/Brand */}
      <div className="h-16 px-6 flex items-center border-b border-[var(--gray-border)]">
        <h1
          className="text-xl font-bold tracking-tight"
          style={{ fontFamily: "'Space Grotesk', sans-serif" }}
        >
          RestMail Admin
        </h1>
      </div>

      {/* Navigation */}
      <nav className="flex-1 px-3 py-4 overflow-y-auto">
        <ul className="space-y-1">
          {navItems.map((item) => {
            const Icon = item.icon;
            const isActive = currentPath === item.to || currentPath.startsWith(item.to + '/');

            return (
              <li key={item.to}>
                <Link
                  to={item.to}
                  className={`
                    flex items-center gap-3 px-3 py-2 rounded-lg transition-colors
                    ${
                      isActive
                        ? 'bg-[var(--red-primary)] text-white'
                        : 'text-[var(--black-soft)] hover:bg-[var(--bg-surface)]'
                    }
                  `}
                  style={{ fontFamily: "'Space Grotesk', sans-serif" }}
                >
                  <Icon className="w-5 h-5" />
                  <span className="font-medium">{item.label}</span>
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>

      {/* Logout */}
      <div className="p-3 border-t border-[var(--gray-border)]">
        <button
          onClick={handleLogout}
          className="w-full flex items-center gap-3 px-3 py-2 rounded-lg text-[var(--black-soft)] hover:bg-[var(--bg-surface)] transition-colors"
          style={{ fontFamily: "'Space Grotesk', sans-serif" }}
        >
          <LogOut className="w-5 h-5" />
          <span className="font-medium">Logout</span>
        </button>
      </div>
    </aside>
  );
}
