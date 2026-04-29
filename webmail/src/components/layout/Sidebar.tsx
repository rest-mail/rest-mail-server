import { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { useAuthStore } from '@/stores/authStore';
import * as api from '@/api/client';
import { Button } from '@/components/ui/button';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { Menu, X, MoreHorizontal, Settings2 } from 'lucide-react';
import { cn } from '@/lib/utils';

const SYSTEM_FOLDERS = ['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk'];

/** 6x6 square indicator — orange when active, muted when inactive */
function Indicator({ active }: { active: boolean }) {
  return (
    <span
      className={cn("w-1.5 h-1.5 shrink-0", active ? "bg-primary" : "bg-muted-foreground")}
    />
  );
}

export function Sidebar() {
  const { user } = useAuthStore();
  const { accounts, folders, activeFolder, activeAccountId, loadingFolders, setActiveAccount, loadAccounts, loadFolders, selectFolder } = useMailStore();
  const { sidebarCollapsed, toggleAccountCollapsed, setView, setSelectedAccountId, startCompose } = useUIStore();
  const [mobileOpen, setMobileOpen] = useState(false);
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState('');
  const [renamingFolder, setRenamingFolder] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState('');

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
    if (renamingFolder) return;
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

  const handleCreateFolder = async () => {
    if (!activeAccountId || !newFolderName.trim()) return;
    try {
      await api.createFolder(activeAccountId, newFolderName.trim());
      setCreatingFolder(false);
      setNewFolderName('');
      await loadFolders();
      toast.success(`Folder "${newFolderName.trim()}" created`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create folder');
    }
  };

  const handleRenameFolder = async (oldName: string) => {
    if (!activeAccountId || !renameValue.trim() || renameValue.trim() === oldName) {
      setRenamingFolder(null);
      return;
    }
    try {
      await api.renameFolder(activeAccountId, oldName, renameValue.trim());
      setRenamingFolder(null);
      await loadFolders();
      if (activeFolder === oldName) {
        selectFolder(renameValue.trim());
      }
      toast.success(`Folder renamed to "${renameValue.trim()}"`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to rename folder');
    }
  };

  const handleDeleteFolder = async (folderName: string) => {
    if (!activeAccountId) return;
    if (!confirm(`Delete folder "${folderName}"? Messages in this folder will be lost.`)) return;
    try {
      await api.deleteFolder(activeAccountId, folderName);
      await loadFolders();
      if (activeFolder === folderName) {
        selectFolder('INBOX');
      }
      toast.success(`Folder "${folderName}" deleted`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete folder');
    }
  };

  const isExpanded = (accountId: number) => {
    return activeAccountId === accountId && !sidebarCollapsed[accountId];
  };

  const sidebarContent = (
    <>
      {/* Logo area */}
      <div className="h-12 flex items-center justify-between px-4 shrink-0">
        <h1 className="font-heading-oswald text-lg font-semibold text-primary tracking-wider">
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

      {/* Compose button */}
      <div className="flex justify-center px-3 pb-2 shrink-0">
        <button
          onClick={() => startCompose()}
          className="w-[200px] h-8 rounded-2xl bg-primary flex items-center justify-center gap-2 hover:opacity-90 transition-opacity"
        >
          <span className="font-mono text-sm font-semibold text-primary-foreground">+</span>
          <span className="font-mono text-xs font-semibold text-primary-foreground">compose</span>
        </button>
      </div>

      {/* Divider */}
      <div className="h-px bg-border shrink-0" />

      {/* Account header */}
      <ScrollArea className="flex-1">
        <div className="py-1">
          {accounts.length === 0 && !loadingFolders && (
            <div className="px-4 py-4 animate-pulse space-y-2">
              <div className="h-3.5 w-36 rounded bg-muted" />
              <div className="ml-4 space-y-1.5">
                <div className="h-3 w-24 rounded bg-muted" />
                <div className="h-3 w-20 rounded bg-muted" />
                <div className="h-3 w-16 rounded bg-muted" />
              </div>
            </div>
          )}
          {accounts.map(account => (
            <div key={account.id}>
              {/* Account row */}
              <div className="group flex items-center hover:bg-sidebar-accent transition-colors">
                <button
                  onClick={() => handleAccountClick(account.id)}
                  className="flex-1 text-left px-4 py-1.5 flex items-center gap-1.5 min-w-0"
                >
                  <span className="text-muted-foreground text-[10px]">
                    {isExpanded(account.id) ? '▾' : '▸'}
                  </span>
                  <span className="truncate flex-1 font-mono text-[11px] font-medium text-foreground">{account.address}</span>
                  {!isExpanded(account.id) && <InboxBadge />}
                </button>
                <button
                  title="Account settings"
                  onClick={() => {
                    setSelectedAccountId(account.id);
                    setView('accountSettings');
                    setMobileOpen(false);
                  }}
                  className="opacity-0 group-hover:opacity-100 transition-opacity p-1.5 mr-1 rounded text-muted-foreground hover:text-foreground"
                >
                  <Settings2 className="w-3.5 h-3.5" />
                </button>
              </div>

              {/* Folder list */}
              {isExpanded(account.id) && (
                <div className="px-1.5 py-1 space-y-px">
                  {loadingFolders ? (
                    <div className="py-1 px-2.5 space-y-1.5 animate-pulse">
                      {Array.from({ length: 4 }).map((_, i) => (
                        <div key={i} className="h-3 w-20 rounded bg-muted" />
                      ))}
                    </div>
                  ) : (
                    <>
                      {folders.map(f => {
                        const isActive = activeFolder === f.name;
                        return (
                          <div key={f.name} className="group flex items-center">
                            {renamingFolder === f.name ? (
                              <input
                                autoFocus
                                value={renameValue}
                                onChange={e => setRenameValue(e.target.value)}
                                onKeyDown={e => {
                                  if (e.key === 'Enter') handleRenameFolder(f.name);
                                  if (e.key === 'Escape') setRenamingFolder(null);
                                }}
                                onBlur={() => handleRenameFolder(f.name)}
                                className="flex-1 px-2.5 py-1 font-mono text-xs bg-transparent border border-input rounded-2xl outline-none"
                              />
                            ) : (
                              <button
                                onClick={() => handleFolderClick(f.name)}
                                className={cn(
                                  "flex-1 text-left px-2.5 h-8 flex items-center gap-2 rounded-2xl transition-all duration-150 font-mono text-[13px]",
                                  isActive
                                    ? "bg-secondary text-foreground font-semibold"
                                    : "text-muted-foreground hover:bg-secondary/50"
                                )}
                              >
                                <Indicator active={isActive} />
                                <span className="flex-1 truncate lowercase">{f.name}</span>
                                {f.unread > 0 && (
                                  <span className="h-4 min-w-[22px] px-1.5 rounded-2xl bg-primary text-[9px] font-bold text-primary-foreground flex items-center justify-center badge-glow">
                                    {f.unread}
                                  </span>
                                )}
                              </button>
                            )}
                            {!SYSTEM_FOLDERS.includes(f.name) && !renamingFolder && (
                              <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                  <button className="p-1 opacity-0 group-hover:opacity-100 transition-opacity text-muted-foreground hover:text-foreground">
                                    <MoreHorizontal className="w-3.5 h-3.5" />
                                  </button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="end">
                                  <DropdownMenuItem onClick={() => {
                                    setRenamingFolder(f.name);
                                    setRenameValue(f.name);
                                  }}>
                                    Rename
                                  </DropdownMenuItem>
                                  <DropdownMenuItem
                                    className="text-destructive"
                                    onClick={() => handleDeleteFolder(f.name)}
                                  >
                                    Delete
                                  </DropdownMenuItem>
                                </DropdownMenuContent>
                              </DropdownMenu>
                            )}
                          </div>
                        );
                      })}

                      {/* Create folder inline */}
                      {creatingFolder ? (
                        <div className="px-2.5 py-1">
                          <input
                            autoFocus
                            value={newFolderName}
                            onChange={e => setNewFolderName(e.target.value)}
                            onKeyDown={e => {
                              if (e.key === 'Enter') handleCreateFolder();
                              if (e.key === 'Escape') { setCreatingFolder(false); setNewFolderName(''); }
                            }}
                            onBlur={() => {
                              if (newFolderName.trim()) handleCreateFolder();
                              else { setCreatingFolder(false); setNewFolderName(''); }
                            }}
                            placeholder="folder_name"
                            className="w-full font-mono text-xs bg-transparent border border-input rounded-2xl px-2.5 py-1 outline-none"
                          />
                        </div>
                      ) : (
                        <button
                          onClick={() => setCreatingFolder(true)}
                          className="w-full text-left px-2.5 py-1 font-mono text-xs text-muted-foreground hover:text-foreground transition-colors"
                        >
                          [+ new_folder]
                        </button>
                      )}
                    </>
                  )}
                </div>
              )}
            </div>
          ))}
        </div>
      </ScrollArea>

      {/* Divider */}
      <div className="h-px bg-border shrink-0" />

      {/* Bottom nav — settings shortcuts */}
      <div className="px-1.5 py-1.5 space-y-px shrink-0">
        {([
          { view: 'vacation' as const, label: 'vacation' },
          { view: 'quarantine' as const, label: 'quarantine' },
          { view: 'tlsReports' as const, label: 'tls_reports' },
          { view: 'pipelineTester' as const, label: 'pipeline_tester' },
          { view: 'pipelineConfig' as const, label: 'pipelines' },
        ] as const).map(item => (
          <button
            key={item.view}
            onClick={() => { setView(item.view); setMobileOpen(false); }}
            className="w-full text-left px-2.5 h-[30px] flex items-center gap-2 rounded-2xl font-mono text-xs text-muted-foreground hover:bg-secondary/50 hover:text-foreground transition-colors"
          >
            <Indicator active={false} />
            {item.label}
          </button>
        ))}
      </div>

      {/* Divider */}
      <div className="h-px bg-border shrink-0" />

      {/* Add Account */}
      <div className="px-3 py-2 shrink-0">
        <button
          onClick={() => { setView('addAccount'); setMobileOpen(false); }}
          className="w-full h-[30px] flex items-center justify-center rounded-2xl font-mono text-[11px] font-medium text-muted-foreground hover:text-foreground transition-colors"
        >
          [+ add_account]
        </button>
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
    <span className="h-4 min-w-[22px] px-1.5 rounded-2xl bg-primary text-[9px] font-bold text-primary-foreground flex items-center justify-center badge-glow font-mono">
      {inbox.unread}
    </span>
  );
}
