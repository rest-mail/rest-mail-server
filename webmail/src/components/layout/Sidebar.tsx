import React, { useEffect, useState } from 'react';
import { toast } from 'sonner';
import { useMailStore } from '@/stores/mailStore';
import { useUIStore } from '@/stores/uiStore';
import { useAuthStore } from '@/stores/authStore';
import * as api from '@/api/client';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
} from '@/components/ui/dropdown-menu';
import { Menu, X, MoreHorizontal, Inbox, Send, FileText, Trash2, AlertTriangle, Archive, Folder, ChevronDown, ChevronRight, Plus, UserPlus } from 'lucide-react';
import { cn } from '@/lib/utils';

const SYSTEM_FOLDERS = ['INBOX', 'Sent', 'Drafts', 'Trash', 'Archive', 'Junk'];

export function Sidebar() {
  const { user } = useAuthStore();
  const { accounts, folders, activeFolder, activeAccountId, loadingFolders, setActiveAccount, loadAccounts, loadFolders, selectFolder } = useMailStore();
  const { sidebarCollapsed, toggleAccountCollapsed, setView } = useUIStore();
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

  const folderIcon = (name: string): React.ReactNode => {
    switch (name) {
      case 'INBOX': return <Inbox className="w-4 h-4" />;
      case 'Sent': return <Send className="w-4 h-4" />;
      case 'Drafts': return <FileText className="w-4 h-4" />;
      case 'Trash': return <Trash2 className="w-4 h-4" />;
      case 'Junk': return <AlertTriangle className="w-4 h-4" />;
      case 'Archive': return <Archive className="w-4 h-4" />;
      default: return <Folder className="w-4 h-4" />;
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
                {isExpanded(account.id) ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
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
                    <>
                      {folders.map(f => (
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
                              className="flex-1 px-3 py-1 text-sm bg-transparent border border-input rounded-md outline-none"
                            />
                          ) : (
                            <button
                              onClick={() => handleFolderClick(f.name)}
                              className={cn(
                                "flex-1 text-left px-3 py-1 text-sm flex items-center gap-2 rounded-md transition-colors",
                                activeFolder === f.name
                                  ? "bg-sidebar-accent text-sidebar-primary font-medium"
                                  : "text-sidebar-foreground hover:bg-sidebar-accent"
                              )}
                            >
                              {folderIcon(f.name)}
                              <span className="flex-1 truncate">{f.name}</span>
                              {f.unread > 0 && (
                                <Badge variant="default" className="text-xs px-1.5 py-0 h-5">
                                  {f.unread}
                                </Badge>
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
                      ))}

                      {/* Create folder inline */}
                      {creatingFolder ? (
                        <div className="px-3 py-1">
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
                            placeholder="Folder name"
                            className="w-full text-sm bg-transparent border border-input rounded-md px-2 py-0.5 outline-none"
                          />
                        </div>
                      ) : (
                        <button
                          onClick={() => setCreatingFolder(true)}
                          className="w-full text-left px-3 py-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
                        >
                          <Plus className="w-3 h-3 mr-1 inline" /> New folder
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

      {/* Add Account */}
      <Separator />
      <div className="p-3">
        <Button
          variant="outline"
          size="sm"
          className="w-full"
          onClick={() => { setView('addAccount'); setMobileOpen(false); }}
        >
          <UserPlus className="w-4 h-4 mr-1" /> Add Account
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
