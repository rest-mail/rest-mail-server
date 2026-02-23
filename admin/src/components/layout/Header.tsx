import { LogOut, Menu } from 'lucide-react';
import { useNavigate } from '@tanstack/react-router';
import { useAuthStore } from '#/lib/stores/authStore';
import { useUIStore } from '#/lib/stores/uiStore';

interface HeaderProps {
  title?: string;
}

export function Header({ title }: HeaderProps) {
  const navigate = useNavigate();
  const user = useAuthStore((state) => state.user);
  const logout = useAuthStore((state) => state.logout);
  const toggleSidebar = useUIStore((state) => state.toggleSidebar);

  const handleLogout = () => {
    logout();
    navigate({ to: '/login' });
  };

  return (
    <header className="h-16 bg-white border-b border-[var(--gray-border)] px-6 flex items-center justify-between">
      {/* Left section */}
      <div className="flex items-center gap-4">
        <button
          onClick={toggleSidebar}
          className="p-2 hover:bg-[var(--bg-surface)] rounded-lg transition-colors"
          aria-label="Toggle sidebar"
        >
          <Menu className="w-5 h-5 text-[var(--black-soft)]" />
        </button>
        {title && (
          <h2
            className="text-xl font-semibold text-[var(--black-soft)]"
            style={{ fontFamily: "'Space Grotesk', sans-serif" }}
          >
            {title}
          </h2>
        )}
      </div>

      {/* Right section */}
      <div className="flex items-center gap-4">
        {user && (
          <div className="flex items-center gap-2">
            <div className="text-right">
              <p className="text-sm font-medium text-[var(--black-soft)]">
                {user.username}
              </p>
              <p className="text-xs text-[var(--gray-secondary)]">
                {user.role}
              </p>
            </div>
          </div>
        )}

        <button
          onClick={handleLogout}
          className="flex items-center gap-2 px-3 py-2 hover:bg-[var(--bg-surface)] rounded-lg transition-colors"
          aria-label="Logout"
        >
          <LogOut className="w-4 h-4 text-[var(--gray-secondary)]" />
          <span className="text-sm font-medium text-[var(--gray-secondary)]">
            Logout
          </span>
        </button>
      </div>
    </header>
  );
}
