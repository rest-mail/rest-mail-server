import { useEffect, useState } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { useAuthStore } from '@/stores/authStore';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import { Menu, X } from 'lucide-react';
import { cn } from '@/lib/utils';

export function Sidebar() {
  const { user } = useAuthStore();
  const { accounts, folders, activeFolder, activeAccountId, loadingFolders, setActiveAccount, loadAccounts, loadFolders, selectFolder } = useMailStore();
  const { sidebarCollapsed, toggleAccountCollapsed, setView } = useUIStore();
  const [mobileOpen, setMobileOpen] = useState(false);

  useEffect(() => {
    if (user) {
      loadAccounts();
    }
  }, [user, loadAccounts]);

  useEffect(() => {
    if (activeAccountId) {
      loadFolders();
    }
  }, [activeAccountId, loadFolders]);

  const handleFolderClick = (folder: string) => {
    setView('mail');
    selectFolder(folder);
    setMobileOpen(false);
  };

  const handleAccountClick = (accountId: number) => {
    if (activeAccountId === accountId) {
      toggleAccountCollapsed(accountId);
    } else {
      setActiveAccount(accountId);
      loadFolders();
      selectFolder('INBOX');
    }
  };

  const folderIcon = (name: string) => {
    switch (name) {
      case 'INBOX': return '\u{1F4E5}';
      case 'Sent': return '\u{1F4E4}';
      case 'Drafts': return '\u{1F4DD}';
      case 'Trash': return '\u{1F5D1}';
      case 'Junk': return '\u26A0';
      case 'Archive': return '\u{1F4E6}';
      default: return '\u{1F4C1}';
    }
  };

  const isExpanded = (accountId: number) => {
    return activeAccountId === accountId && !sidebarCollapsed[accountId];
  };

  const sidebarContent = (
    <>
      {/* Logo */}
      <div className="px-4 py-4 flex items-center justify-between">
        <h1 className="text-lg font-bold tracking-tight text-sidebar-primary">
          REST MAIL
        </h1>
        <Button
          variant="ghost"
          size="sm"
          className="md:hidden h-8 w-8 p-0"
          onClick={() => setMobileOpen(false)}
        >
          <X className="w-4 h-4" />
        </Button>
      </div>
      <Separator />

      {/* Account tree */}
      <ScrollArea className="flex-1">
        <div className="py-2">
          {accounts.length === 0 && !loadingFolders && (
            <div className="px-3 py-4 animate-pulse space-y-2">
              <div className="h-4 w-36 rounded bg-muted" />
              <div className="ml-4 space-y-1.5">
                <div className="h-3.5 w-24 rounded bg-muted" />
                <div className="h-3.5 w-20 rounded bg-muted" />
                <div className="h-3.5 w-16 rounded bg-muted" />
              </div>
            </div>
          )}
          {accounts.map(account => (
            <div key={account.id}>
              <button
                onClick={() => handleAccountClick(account.id)}
                className={cn(
                  "w-full text-left px-3 py-1.5 text-sm flex items-center gap-1.5",
                  "hover:bg-sidebar-accent transition-colors text-sidebar-foreground"
                )}
              >
                <span className="text-xs">{isExpanded(account.id) ? '\u25BC' : '\u25B6'}</span>
                <span className="truncate flex-1 font-medium">{account.address}</span>
                {!isExpanded(account.id) && <InboxBadge />}
              </button>

              {isExpanded(account.id) && (
                <div className="ml-4">
                  {loadingFolders ? (
                    <div className="py-1 px-3 space-y-1.5 animate-pulse">
                      {Array.from({ length: 4 }).map((_, i) => (
                        <div key={i} className="h-3.5 w-20 rounded bg-muted" />
                      ))}
                    </div>
                  ) : (
                    folders.map(f => (
                      <button
                        key={f.name}
                        onClick={() => handleFolderClick(f.name)}
                        className={cn(
                          "w-full text-left px-3 py-1 text-sm flex items-center gap-2 rounded-md transition-colors",
                          activeFolder === f.name
                            ? "bg-sidebar-accent text-sidebar-primary font-medium"
                            : "text-sidebar-foreground hover:bg-sidebar-accent"
                        )}
                      >
                        <span className="text-xs">{folderIcon(f.name)}</span>
                        <span className="flex-1 truncate">{f.name}</span>
                        {f.unread > 0 && (
                          <Badge variant="default" className="text-xs px-1.5 py-0 h-5">
                            {f.unread}
                          </Badge>
                        )}
                      </button>
                    ))
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </ScrollArea>

      {/* Add Account */}
      <Separator />
      <div className="p-3">
        <Button
          variant="outline"
          size="sm"
          className="w-full"
          onClick={() => { setView('addAccount'); setMobileOpen(false); }}
        >
          + Add Account
        </Button>
      </div>
    </>
  );

  return (
    <>
      {/* Mobile menu button */}
      <Button
        variant="ghost"
        size="sm"
        className="md:hidden fixed top-3 left-3 z-50 h-8 w-8 p-0"
        onClick={() => setMobileOpen(true)}
      >
        <Menu className="w-4 h-4" />
      </Button>

      {/* Mobile overlay */}
      {mobileOpen && (
        <div
          className="md:hidden fixed inset-0 bg-black/50 z-40"
          onClick={() => setMobileOpen(false)}
        />
      )}

      {/* Sidebar panel */}
      <div
        className={cn(
          "h-full flex flex-col border-r bg-sidebar w-60 min-w-60",
          "max-md:fixed max-md:inset-y-0 max-md:left-0 max-md:z-50 max-md:shadow-lg max-md:transition-transform max-md:duration-200",
          mobileOpen ? "max-md:translate-x-0" : "max-md:-translate-x-full"
        )}
      >
        {sidebarContent}
      </div>
    </>
  );
}

function InboxBadge() {
  const { folders } = useMailStore();
  const inbox = folders.find(f => f.name === 'INBOX');
  if (!inbox || inbox.unread === 0) return null;
  return (
    <Badge variant="default" className="text-xs px-1.5 py-0 h-5">
      {inbox.unread}
    </Badge>
  );
}
