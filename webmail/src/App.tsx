import { useEffect, useCallback } from 'react';
import { toast, Toaster } from 'sonner';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { setOnUnauthorized } from '@/api/client';
import { LoginPage } from '@/components/auth/LoginPage';
import { Sidebar } from '@/components/layout/Sidebar';
import { TopBar } from '@/components/layout/TopBar';
import { MessageList } from '@/components/mail/MessageList';
import { MessageViewer } from '@/components/mail/MessageViewer';
import { ComposeView } from '@/components/compose/ComposeView';
import { AddAccountView } from '@/components/account/AddAccountView';
import { AccountDetailsView } from '@/components/account/AccountDetailsView';
import { VacationView } from '@/components/settings/VacationView';
import { QuarantineView } from '@/components/settings/QuarantineView';
import { TLSReportsView } from '@/components/admin/TLSReportsView';
import { PipelineTesterView } from '@/components/admin/PipelineTesterView';
import { PipelineConfigView } from '@/components/admin/PipelineConfigView';
import { SettingsView } from '@/components/settings/SettingsView';
import { AccountSettingsView } from '@/components/account/AccountSettingsView';
import { useMultiAccountSSE, type SSEEvent } from '@/hooks/useSSE';
import { useNotifications } from '@/hooks/useNotifications';

function App() {
  const { isAuthenticated, logout } = useAuthStore();
  const { view, startCompose } = useUIStore();
  const { accounts, refresh, loadFolders } = useMailStore();
  // Wire up 401 handler to auto-logout
  useEffect(() => {
    setOnUnauthorized(() => {
      toast.error('Session expired. Please log in again.');
      logout();
    });
  }, [logout]);
  const { requestPermission, showDesktopNotification } = useNotifications();

  // Request notification permission on first auth
  useEffect(() => {
    if (isAuthenticated) {
      requestPermission();
    }
  }, [isAuthenticated, requestPermission]);

  // SSE event handler
  const handleSSEEvent = useCallback((event: SSEEvent) => {
    if (event.type === 'new_message') {
      const data = event.data as { sender?: string; subject?: string };
      toast.info(`New message from ${data.sender || 'unknown'}`, {
        description: data.subject || '(no subject)',
      });
      showDesktopNotification(
        `New mail from ${data.sender || 'unknown'}`,
        data.subject as string || '(no subject)',
      );
      refresh();
    } else if (event.type === 'folder_update') {
      loadFolders();
    } else if (event.type === 'message_updated' || event.type === 'message_deleted') {
      refresh();
    } else if (event.type === 'message_sent') {
      refresh();
    }
  }, [refresh, loadFolders, showDesktopNotification]);

  // Subscribe to SSE events for all linked accounts
  const accountIds = isAuthenticated ? accounts.map(a => a.id) : [];
  useMultiAccountSSE(accountIds, handleSSEEvent);

  // Keyboard shortcuts
  useEffect(() => {
    if (!isAuthenticated) return;
    const handleKeyDown = (e: KeyboardEvent) => {
      // Ignore shortcuts when typing in inputs
      const tag = (e.target as HTMLElement).tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target as HTMLElement).isContentEditable) return;

      if (e.key === 'c' && !e.metaKey && !e.ctrlKey) {
        e.preventDefault();
        startCompose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isAuthenticated, startCompose]);

  if (!isAuthenticated) {
    return (
      <>
        <LoginPage />
        <Toaster richColors position="bottom-right" />
      </>
    );
  }

  return (
    <>
      <div className="h-full flex">
        {/* Left sidebar */}
        <Sidebar />

        {/* Right panel */}
        <div className="flex-1 flex flex-col min-w-0">
          <TopBar />

          {/* Content area */}
          <div className="flex-1 overflow-hidden">
            {view === 'mail' && <div className="h-full animate-fade-in"><MailView /></div>}
            {view === 'compose' && <div className="h-full animate-fade-in"><ComposeView /></div>}
            {view === 'addAccount' && <div className="h-full animate-fade-in"><AddAccountView /></div>}
            {view === 'accountDetails' && <div className="h-full animate-fade-in"><AccountDetailsView /></div>}
            {view === 'vacation' && <div className="h-full animate-fade-in"><VacationView /></div>}
            {view === 'quarantine' && <div className="h-full animate-fade-in"><QuarantineView /></div>}
            {view === 'tlsReports' && <div className="h-full animate-fade-in"><TLSReportsView /></div>}
            {view === 'pipelineTester' && <div className="h-full animate-fade-in"><PipelineTesterView /></div>}
            {view === 'pipelineConfig' && <div className="h-full animate-fade-in"><PipelineConfigView /></div>}
            {view === 'settings'        && <div className="h-full"><SettingsView /></div>}
            {view === 'accountSettings' && <div className="h-full"><AccountSettingsView /></div>}
          </div>
        </div>
      </div>
      <Toaster richColors position="bottom-right" />
    </>
  );
}

function MailView() {
  return (
    <div className="h-full flex flex-col lg:flex-row">
      {/* Message list: top half on <lg, fixed 380px column on lg+ */}
      <div className="h-1/2 lg:h-full lg:w-[380px] shrink-0 min-w-0 border-b lg:border-b-0 lg:border-r border-border overflow-hidden">
        <MessageList />
      </div>
      {/* Message viewer: bottom half on <lg, flex remaining on lg+ */}
      <div className="flex-1 min-h-0 min-w-0 overflow-hidden">
        <MessageViewer />
      </div>
    </div>
  );
}

export default App;
