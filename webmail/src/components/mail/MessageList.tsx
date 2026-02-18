import { useEffect, useCallback } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Badge } from '@/components/ui/badge';
import { cn } from '@/lib/utils';
import { Inbox, Mail, Search, X } from 'lucide-react';

export function MessageList() {
  const {
    messages, selectedMessageId, loadingMessages, activeFolder,
    selectMessage, hasMore, loadMoreMessages,
    searchResults, searchQuery, isSearching, clearSearch,
  } = useMailStore();

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
      return 'Yesterday';
    }
    return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
  };

  if (loadingMessages && !isInSearch) {
    return (
      <div className="divide-y divide-border">
        {Array.from({ length: 8 }).map((_, i) => (
          <div key={i} className="px-4 py-2.5 flex items-start gap-3 animate-pulse">
            <div className="mt-0.5 w-4 h-4 rounded bg-muted" />
            <div className="flex-1 space-y-2">
              <div className="flex items-center justify-between">
                <div className="h-3.5 w-32 rounded bg-muted" />
                <div className="h-3 w-14 rounded bg-muted" />
              </div>
              <div className="h-3.5 w-48 rounded bg-muted" />
            </div>
          </div>
        ))}
      </div>
    );
  }

  if (displayMessages.length === 0) {
    if (isInSearch) {
      return (
        <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
          <Search className="w-10 h-10 stroke-1" />
          <p className="text-sm">No results for "{searchQuery}"</p>
          <button
            onClick={clearSearch}
            className="text-sm text-primary hover:underline"
          >
            Clear search
          </button>
        </div>
      );
    }
    return (
      <div className="flex flex-col items-center justify-center h-full text-muted-foreground gap-3">
        <Inbox className="w-10 h-10 stroke-1" />
        <p className="text-sm">No messages in {activeFolder}</p>
      </div>
    );
  }

  return (
    <ScrollArea className="h-full">
      <div className="divide-y divide-border">
        {isInSearch && (
          <div className="flex items-center justify-between px-4 py-2 bg-muted/50 text-sm text-muted-foreground">
            <span className="flex items-center gap-1.5">
              <Search className="h-3.5 w-3.5" />
              {isSearching ? 'Searching...' : `${searchResults.length} result${searchResults.length === 1 ? '' : 's'} for "${searchQuery}"`}
            </span>
            <button
              onClick={clearSearch}
              className="flex items-center gap-1 text-primary hover:underline"
            >
              <X className="h-3.5 w-3.5" />
              Clear
            </button>
          </div>
        )}
        {displayMessages.map(msg => (
          <button
            key={msg.id}
            onClick={() => selectMessage(msg.id)}
            className={cn(
              "w-full text-left px-4 py-2.5 flex items-start gap-3 transition-colors",
              selectedMessageId === msg.id ? "bg-accent" : "hover:bg-muted",
            )}
          >
            {/* Star indicator */}
            <span className="mt-0.5 text-sm">
              {msg.is_flagged ? '\u2605' : '\u2606'}
            </span>

            {/* Content */}
            <div className="flex-1 min-w-0">
              <div className="flex items-center justify-between gap-2">
                <span className={cn(
                  "text-sm truncate",
                  !msg.is_read ? "font-semibold text-foreground" : "text-muted-foreground"
                )}>
                  {msg.sender_name || msg.sender}
                </span>
                <span className="text-xs text-muted-foreground whitespace-nowrap">
                  {formatDate(msg.received_at)}
                </span>
              </div>
              <div className="flex items-center gap-2">
                <span className={cn(
                  "text-sm truncate",
                  !msg.is_read ? "font-medium text-foreground" : "text-muted-foreground"
                )}>
                  {msg.subject || '(no subject)'}
                </span>
                {msg.has_attachments && (
                  <Badge variant="secondary" className="text-xs px-1 py-0 h-4 shrink-0">
                    {"\u{1F4CE}"}
                  </Badge>
                )}
              </div>
            </div>
          </button>
        ))}

        {hasMore && (
          <button
            onClick={() => loadMoreMessages()}
            className="w-full py-3 text-sm text-primary hover:bg-muted transition-colors"
          >
            Load more messages...
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
      <p className="text-sm">Select a message to read</p>
    </div>
  );
}
