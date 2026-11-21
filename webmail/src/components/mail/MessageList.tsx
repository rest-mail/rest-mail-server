import { displayAddress } from '@/lib/addressDisplay';
import { mailPlatform } from '@/platform';
import { MessageActions } from './MessageActions';
import { useEffect, useCallback, useState } from 'react';
import { visibleMessages } from '@/lib/messageClass';
import { useMailStore } from '@/stores/mailStore';
import { useSettingsStore } from '@/stores/settingsStore';
import { useUIStore } from '@/stores/uiStore';
import { getThemePack, TYPEFACE_CLASS, avatarTint, initialsOf } from '@/theme/packs';
import { ScrollArea } from '@/components/ui/scroll-area';
import { ClassLabel } from '@/components/mail/signals';
import { cn } from '@/lib/utils';
import { Clock, Mail, X, Paperclip } from 'lucide-react';
import { WaitingBadge } from './signals';
import { updateMessage, deleteMessage } from '@/api/client';

/** Count total unread across displayed messages */
function useUnreadCount() {
  const { messages, searchResults, waitingMessages } = useMailStore();
  const display = waitingMessages ?? searchResults ?? messages;
  return display.filter(m => !m.is_read).length;
}

export function MessageList() {
  const {
    messages, selectedMessageId, loadingMessages, activeFolder,
    selectMessage, hasMore, loadMoreMessages,
    searchResults, searchQuery, clearSearch,
    waitingMessages, waitingHasMore, showWaiting, loadMoreWaiting, clearWaiting,
  } = useMailStore();
  const { density } = useSettingsStore();
  const pack = useUIStore(s => getThemePack(s.theme));
  const v = pack.variants;
  const unreadCount = useUnreadCount();

  const isInSearch = searchResults !== null;
  const [collapseLists, setCollapseLists] = useState(false);
  const [sort, setSort] = useState('newest');
  const [threaded, setThreaded] = useState(false);
  const [selected, setSelected] = useState<number[]>([]);
  const desktop = mailPlatform().desktop;
  useEffect(() => setSelected([]), [activeFolder, searchQuery]);
  const isWaiting = waitingMessages !== null;
  const allMessages = waitingMessages ?? (isInSearch ? searchResults : messages);
  let displayMessages = visibleMessages(allMessages, collapseLists && !isInSearch && !isWaiting, selectedMessageId);
  if (desktop) {
    displayMessages = [...displayMessages].sort((first, second) => sort === 'subject' ? first.subject.localeCompare(second.subject) : sort === 'sender' ? first.sender.localeCompare(second.sender) : (sort === 'oldest' ? 1 : -1) * (Date.parse(first.received_at) - Date.parse(second.received_at)));
    if (threaded) {
      const seen = new Set<string>();
      displayMessages = displayMessages.filter(message => {
        const thread = message.thread_id || String(message.id);
        if (seen.has(thread)) return false;
        seen.add(thread); return true;
      });
    }
  }
  const listCount = allMessages.filter(message => message.class === 'newsletter').length;

  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    if (!displayMessages.length) return;
    const tag = (e.target as HTMLElement).tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target as HTMLElement).isContentEditable) return;

    const idx = displayMessages.findIndex(m => m.id === selectedMessageId);
    if (desktop && !e.metaKey && !e.ctrlKey && !e.altKey && selectedMessageId && (e.key === 'Delete' || e.key.toLowerCase() === 'e' || e.key === 'Enter')) {
      e.preventDefault();
      if (e.key === 'Enter') void selectMessage(selectedMessageId);
      else void (e.key === 'Delete' ? deleteMessage(selectedMessageId) : updateMessage(selectedMessageId, { folder: 'Archive' })).then(() => useMailStore.getState().refresh());
      return;
    }

    if (e.key === 'ArrowDown' || e.key === 'j') {
      e.preventDefault();
      const next = Math.min(idx + 1, displayMessages.length - 1);
      selectMessage(displayMessages[next].id);
    } else if (e.key === 'ArrowUp' || e.key === 'k') {
      e.preventDefault();
      const prev = Math.max(idx - 1, 0);
      selectMessage(displayMessages[prev].id);
    }
  }, [displayMessages, selectedMessageId, selectMessage, desktop]);

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
  if (allMessages.length === 0) {
    if (isWaiting) {
      return (
        <div className={cn('flex flex-col items-center justify-center h-full text-muted-foreground gap-3', TYPEFACE_CLASS[v.listType])}>
          <p className="text-sm">{desktop ? 'No messages waiting for a reply.' : '// nothing_waiting_on_a_reply'}</p>
          <button onClick={clearWaiting} className="text-xs text-primary hover:underline">
            {desktop ? 'Back to messages' : `[back_to_${activeFolder?.toLowerCase()}]`}
          </button>
        </div>
      );
    }
    if (isInSearch) {
      return (
        <div className={cn('flex flex-col items-center justify-center h-full text-muted-foreground gap-3', TYPEFACE_CLASS[v.listType])}>
          <p className="text-sm">{desktop ? `No results for “${searchQuery}”.` : `// no_results for "${searchQuery}"`}</p>
          <button
            onClick={clearSearch}
            className="text-xs text-primary hover:underline"
          >
            {desktop ? 'Clear search' : '[clear_search]'}
          </button>
        </div>
      );
    }
    return (
      <div className={cn('flex flex-col items-center justify-center h-full text-muted-foreground gap-3', TYPEFACE_CLASS[v.listType])}>
        <p className="text-sm">{desktop ? `No messages in ${activeFolder === 'INBOX' ? 'Inbox' : activeFolder}.` : `// no_messages in ${activeFolder?.toLowerCase()}`}</p>
      </div>
    );
  }

  return (
    <ScrollArea className={cn('h-full', TYPEFACE_CLASS[v.listType], desktop && 'desktop-list-scroll')}>
      <div className={cn('flex flex-col', v.surface === 'cards' && 'py-1')}>
        {desktop && <>
          <div className="desktop-list-controls flex gap-3 px-3 py-2 text-xs items-center">
            <input data-testid="messages-select-all" aria-label="Select all messages" type="checkbox" checked={displayMessages.length > 0 && displayMessages.every(message => selected.includes(message.id))} onChange={event => setSelected(event.target.checked ? displayMessages.map(message => message.id) : [])} />
            <select data-testid="messages-sort" aria-label="Sort messages" value={sort} onChange={event => setSort(event.target.value)}><option value="newest">Newest first</option><option value="oldest">Oldest first</option><option value="subject">Subject</option><option value="sender">Sender</option></select>
            <label><input data-testid="messages-threaded" type="checkbox" checked={threaded} onChange={event => setThreaded(event.target.checked)} /> Threads</label>
          </div>
          {selected.length > 0 && <MessageActions ids={selected} prefix="bulk" onComplete={() => setSelected([])} />}
        </>}
        {/* List header */}
        <div className={cn('flex items-center justify-between h-8 px-3 shrink-0', desktop && 'desktop-list-heading')}>
          <span className={cn(
            "text-sm text-muted-foreground",
            v.listType === 'serif' ? 'font-semibold italic' : 'font-heading-oswald font-semibold tracking-wide',
          )}>
            {desktop ? (isWaiting ? 'Waiting' : isInSearch ? 'Search results' : activeFolder === 'INBOX' ? 'Inbox' : activeFolder) : isWaiting ? 'WAITING' : isInSearch ? 'SEARCH' : activeFolder}
          </span>
          <span className="flex items-center gap-2">
            {isWaiting ? (
              <button onClick={clearWaiting} className="flex items-center gap-1 text-[9px] font-medium text-primary">
                <X className="w-3 h-3" /> clear
              </button>
            ) : (
              <button onClick={() => void showWaiting()} className="flex items-center gap-1 text-[9px] font-medium text-muted-foreground hover:text-primary">
                <Clock className="w-3 h-3" /> waiting
              </button>
            )}
            {isWaiting ? null : isInSearch ? (
              <button onClick={clearSearch} className="flex items-center gap-1 text-[9px] font-medium text-primary">
                <X className="w-3 h-3" /> clear
              </button>
            ) : unreadCount > 0 ? (
              <span className="text-[9px] font-medium text-primary">
                {desktop ? `${unreadCount} unread` : `// ${unreadCount}_UNREAD`}
              </span>
            ) : null}
          </span>
        </div>

        {/* Divider */}
        {!isInSearch && !isWaiting && listCount > 0 && (
          <button
            onClick={() => setCollapseLists(collapsed => !collapsed)}
            aria-pressed={collapseLists}
            className="w-full px-3 py-2 text-xs text-primary text-left hover:bg-secondary/30"
          >
            {collapseLists ? 'Show' : 'Collapse'} list mail ({listCount})
          </button>
        )}
        <div className="h-px bg-border" />

        {/* Messages */}
        {displayMessages.map((msg, i) => (
          <div key={msg.id} className={desktop ? 'desktop-message flex items-start' : undefined} data-unread={desktop && !msg.is_read ? 'true' : undefined} data-selected={desktop && selectedMessageId === msg.id ? 'true' : undefined}>
            {desktop && <input data-testid={`message-select-${msg.id}`} aria-label={`Select ${msg.subject}`} className="ml-2 mt-3" type="checkbox" checked={selected.includes(msg.id)} onChange={event => setSelected(previous => event.target.checked ? [...previous, msg.id] : previous.filter(id => id !== msg.id))} />}
            <button
              data-testid={`message-${msg.id}`}
              onClick={() => selectMessage(msg.id)}
              className={cn(
                "w-full text-left px-3 flex items-start gap-2 transition-all duration-150",
                desktop && 'desktop-message-row',
                density === 'compact' ? 'py-1.5' : 'py-2',
                selectedMessageId === msg.id ? "bg-secondary" : "hover:bg-secondary/30",
                (v.surface === 'rules' || v.surface === 'dense') && 'border-t border-border',
                v.surface === 'dense' && !msg.is_read && 'bg-secondary/40',
                v.surface === 'cards' && 'mx-2 my-1 rounded-xl border border-border bg-card',
                v.unread === 'rail' && !msg.is_read && 'unread-rail',
              )}
            >
              {/* Sender identity chip (packs that want one) */}
              {!desktop && v.identity !== 'none' && (
                <span
                  className={cn(
                    "mt-0.5 w-6 h-6 shrink-0 flex items-center justify-center text-[9px] font-bold text-white select-none",
                    v.identity === 'circle' ? 'rounded-full' : 'rounded-lg',
                  )}
                  style={{ background: avatarTint(msg.sender) }}
                >
                  {initialsOf(msg.sender_name || displayAddress(msg.sender))}
                </span>
              )}

              {/* Star */}
              <span className={cn(
                "text-xs mt-0.5 shrink-0", desktop && 'desktop-row-star',
                msg.is_flagged ? "text-primary" : "text-muted-foreground"
              )}>
                {msg.is_flagged ? '★' : '☆'}
              </span>

              {/* Content */}
              <div className="flex-1 min-w-0 space-y-0.5">
                <div className="flex items-center justify-between gap-2">
                  <span className={cn(
                    "text-xs truncate flex items-center gap-1.5 min-w-0",
                    !msg.is_read ? "font-semibold text-foreground" : "text-muted-foreground"
                  )}>
                    {v.unread === 'dot' && !msg.is_read && (
                      <span className="w-1.5 h-1.5 rounded-full bg-primary shrink-0" />
                    )}
                    <span className="truncate">{msg.sender_name || displayAddress(msg.sender)}</span>
                  </span>
                  <span className="text-[10px] text-muted-foreground whitespace-nowrap">
                    {formatDate(msg.received_at)}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <span className={cn(
                    "text-[13px] truncate",
                    !msg.is_read ? "font-medium text-foreground" : "text-muted-foreground"
                  )}>
                    {msg.subject || '(no subject)'}
                  </span>
                  <ClassLabel messageClass={msg.class} />
                  {msg.has_attachments && (
                    <Paperclip className="w-3 h-3 text-muted-foreground shrink-0" />
                  )}
                </div>
                {desktop && <p className="desktop-message-preview">{msg.body_text?.replace(/\s+/g, ' ') || (msg.has_attachments ? 'Includes attachments' : '')}</p>}
                <WaitingBadge since={msg.awaiting_since} />
              </div>
            </button>
            {/* Divider between messages */}
            {v.surface === 'dividers' && i < displayMessages.length - 1 && (
              <div className="h-px bg-border/50" />
            )}
          </div>
        ))}

        {(isWaiting ? waitingHasMore : hasMore) && (
          <button
            data-testid="messages-load-more"
            onClick={() => (isWaiting ? loadMoreWaiting() : loadMoreMessages())}
            className="w-full py-3 text-xs text-primary hover:bg-secondary/30 transition-colors"
          >
            {desktop ? 'Load more messages' : '[load_more...]'}
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
      <p className="font-mono text-sm">{mailPlatform().desktop ? 'Select a message to read it.' : '// select_a_message'}</p>
    </div>
  );
}
