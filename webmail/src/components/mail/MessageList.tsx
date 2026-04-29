import { useEffect, useCallback } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { useSettingsStore } from '@/stores/settingsStore';
import { ScrollArea } from '@/components/ui/scroll-area';
import { cn } from '@/lib/utils';
import { Mail, X, Paperclip } from 'lucide-react';

/** Count total unread across displayed messages */
function useUnreadCount() {
  const { messages, searchResults } = useMailStore();
  const display = searchResults ?? messages;
  return display.filter(m => !m.is_read).length;
}

export function MessageList() {
  const {
    messages, selectedMessageId, loadingMessages, activeFolder,
    selectMessage, hasMore, loadMoreMessages,
    searchResults, searchQuery, clearSearch,
  } = useMailStore();
  const { density } = useSettingsStore();
  const unreadCount = useUnreadCount();

  const isInSearch = searchResults !== null;
  const displayMessages = isInSearch ? searchResults : messages;

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (!displayMessages.length) return;
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target as HTMLElement).isContentEditable) return;

    const idx = displayMessages.findIndex(m => m.id === selectedMessageId);

    if (e.key === 'ArrowDown' || e.key === 'j') {
      e.preventDefault();
      const next = Math.min(idx + 1, displayMessages.length - 1);
      selectMessage(displayMessages[next].id);
    } else if (e.key === 'ArrowUp' || e.key === 'k') {
      e.preventDefault();
      const prev = Math.max(idx - 1, 0);
      selectMessage(displayMessages[prev].id);
    }
  }, [displayMessages, selectedMessageId, selectMessage]);

  useEffect(() => {
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleKeyDown]);

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr);
    const now = new Date();
    const isToday = date.toDateString() === now.toDateString();
    if (isToday) {
      return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    }
    const yesterday = new Date(now);
    yesterday.setDate(yesterday.getDate() - 1);
    if (date.toDateString() === yesterday.toDateString()) {
      return 'yesterday';
    }
    return date.toLocaleDateString([], { month: 'short', day: 'numeric' }).toLowerCase().replace(' ', '_');
  };

  // Loading skeleton
  if (loadingMessages && !isInSearch) {
    return (
      <div className="flex flex-col">
        {Array.from({ length: 8 }).map((_, i) => (
          <div key={i} className="px-3 py-2 flex items-start gap-2">
            <div className="mt-0.5 w-3 h-3 rounded shimmer" />
            <div className="flex-1 space-y-1.5">
              <div className="flex items-center justify-between">
                <div className="h-3 w-32 rounded shimmer" />
                <div className="h-2.5 w-14 rounded shimmer" />
              </div>
              <div className="h-3 w-48 rounded shimmer" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  // Empty state
  if (displayMessages.length === 0) {
    if (isInSearch) {
      return (
        <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
          <p className="font-mono text-sm">// no_results for "{searchQuery}"</p>
          <button
            onClick={clearSearch}
            className="font-mono text-xs text-primary hover:underline"
          >
            [clear_search]
          </button>
        </div>
      );
    }
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <p className="font-mono text-sm">// no_messages in {activeFolder?.toLowerCase()}</p>
      </div>
    );
  }

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col">
        {/* List header */}
        <div className="flex items-center justify-between h-8 px-3 shrink-0">
          <span className="font-heading-oswald text-sm font-semibold tracking-wide text-muted-foreground">
            {isInSearch ? 'SEARCH' : activeFolder}
          </span>
          {isInSearch ? (
            <button onClick={clearSearch} className="flex items-center gap-1 font-mono text-[9px] font-medium text-primary">
              <X className="w-3 h-3" /> clear
            </button>
          ) : unreadCount > 0 ? (
            <span className="font-mono text-[9px] font-medium text-primary">
              // {unreadCount}_UNREAD
            </span>
          ) : null}
        </div>

        {/* Divider */}
        <div className="h-px bg-border" />

        {/* Messages */}
        {displayMessages.map((msg, i) => (
          <div key={msg.id}>
            <button
              onClick={() => selectMessage(msg.id)}
              className={cn(
                "w-full text-left px-3 flex items-start gap-2 transition-all duration-150",
                density === 'compact' ? 'py-1.5' : 'py-2',
                selectedMessageId === msg.id ? "bg-secondary" : "hover:bg-secondary/30",
              )}
            >
              {/* Star */}
              <span className={cn(
                "text-xs mt-0.5 shrink-0",
                msg.is_flagged ? "text-primary" : "text-muted-foreground"
              )}>
                {msg.is_flagged ? '★' : '☆'}
              </span>

              {/* Content */}
              <div className="flex-1 min-w-0 space-y-0.5">
                <div className="flex items-center justify-between gap-2">
                  <span className={cn(
                    "font-mono text-xs truncate",
                    !msg.is_read ? "font-semibold text-foreground" : "text-muted-foreground"
                  )}>
                    {msg.sender_name || msg.sender}
                  </span>
                  <span className="font-mono text-[10px] text-muted-foreground whitespace-nowrap">
                    {formatDate(msg.received_at)}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <span className={cn(
                    "font-mono text-[13px] truncate",
                    !msg.is_read ? "font-medium text-foreground" : "text-muted-foreground"
                  )}>
                    {msg.subject || '(no subject)'}
                  </span>
                  {msg.has_attachments && (
                    <Paperclip className="w-3 h-3 text-muted-foreground shrink-0" />
                  )}
                </div>
              </div>
            </button>
            {/* Divider between messages */}
            {i < displayMessages.length - 1 && (
              <div className="h-px bg-border/50" />
            )}
          </div>
        ))}

        {hasMore && (
          <button
            onClick={() => loadMoreMessages()}
            className="w-full py-3 font-mono text-xs text-primary hover:bg-secondary/30 transition-colors"
          >
            [load_more...]
          </button>
        )}
      </div>
    </ScrollArea>
  );
}

export function EmptyMailViewer() {
  return (
    <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
      <Mail className="w-10 h-10 stroke-1" />
      <p className="font-mono text-sm">// select_a_message</p>
    </div>
  );
}
