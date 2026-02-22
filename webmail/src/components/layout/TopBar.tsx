import { useState, useCallback, useRef, useEffect } from 'react';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore, type Theme } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { X, Loader2, Check, RefreshCw, Palette, Settings } from 'lucide-react';
import { cn } from '@/lib/utils';

// Returns 1-2 uppercase initials from an email address.
function initials(email: string | undefined): string {
  if (!email) return '?';
  const local = email.split('@')[0];
  const parts = local.split(/[._-]/);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return local.slice(0, 2).toUpperCase();
}

type PaletteGroup = { label: string; themes: { id: Theme; name: string; bg: string; accent: string }[] };

const PALETTE_GROUPS: PaletteGroup[] = [
  {
    label: 'Tech',
    themes: [
      { id: 'industrial', name: 'Industrial', bg: '#1a1a1a', accent: '#FF6B35' },
    ],
  },
  {
    label: 'Light',
    themes: [
      { id: 'dawn',   name: 'Dawn',   bg: '#ffffff', accent: '#2563eb' },
      { id: 'linen',  name: 'Linen',  bg: '#fdf6e3', accent: '#c27835' },
    ],
  },
  {
    label: 'Mid',
    themes: [
      { id: 'slate',  name: 'Slate',  bg: '#1e293b', accent: '#818cf8' },
      { id: 'dusk',   name: 'Dusk',   bg: '#2a1f1a', accent: '#e07b4f' },
    ],
  },
  {
    label: 'Dark',
    themes: [
      { id: 'midnight', name: 'Midnight', bg: '#111827', accent: '#3b82f6' },
      { id: 'forest',   name: 'Forest',   bg: '#0d1f0d', accent: '#4ade80' },
    ],
  },
  {
    label: 'Vivid',
    themes: [
      { id: 'neon',    name: 'Neon',    bg: '#0a0a1a', accent: '#06d6a0' },
      { id: 'aurora',  name: 'Aurora',  bg: '#0d0d1a', accent: '#38bdf8' },
    ],
  },
];

export function TopBar() {
  const { user, logout } = useAuthStore();
  const { theme, setTheme, setView } = useUIStore();
  const { refresh, searchMessages, clearSearch, isSearching, searchQuery } = useMailStore();

  const [inputValue, setInputValue] = useState('');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!searchQuery && inputValue) setInputValue('');
  }, [searchQuery]); // eslint-disable-line react-hooks/exhaustive-deps

  const debouncedSearch = useCallback((value: string) => {
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      value.trim() ? searchMessages(value) : clearSearch();
    }, 300);
  }, [searchMessages, clearSearch]);

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setInputValue(e.target.value);
    debouncedSearch(e.target.value);
  };

  const handleClearSearch = () => {
    setInputValue('');
    clearSearch();
    inputRef.current?.focus();
  };

  return (
    <div className="flex items-center gap-2 h-12 px-4 bg-secondary shrink-0">
      {/* Search bar */}
      <div className="flex-1 min-w-0 hidden sm:block">
        <div className="relative flex items-center h-8 rounded-2xl bg-background px-3 gap-2">
          <span className="text-muted-foreground text-[13px] shrink-0">🔍</span>
          <input
            ref={inputRef}
            type="text"
            placeholder="search_emails..."
            value={inputValue}
            onChange={handleInputChange}
            onKeyDown={e => e.key === 'Escape' && handleClearSearch()}
            className="flex-1 bg-transparent font-mono text-xs text-foreground placeholder:text-muted-foreground outline-none min-w-0"
          />
          {isSearching
            ? <Loader2 className="h-3.5 w-3.5 text-muted-foreground animate-spin shrink-0" />
            : inputValue
              ? <button onClick={handleClearSearch} className="text-muted-foreground hover:text-foreground shrink-0">
                  <X className="h-3.5 w-3.5" />
                </button>
              : null}
        </div>
      </div>

      {/* Refresh button */}
      <button
        onClick={() => refresh()}
        className="w-8 h-8 rounded-2xl bg-background flex items-center justify-center text-muted-foreground hover:text-foreground transition-colors shrink-0"
        title="Refresh"
      >
        <RefreshCw className="w-3.5 h-3.5" />
      </button>

      {/* Separator */}
      <div className="w-px h-5 bg-border shrink-0" />

      {/* User menu */}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button className="flex items-center gap-2 shrink-0 hover:opacity-80 transition-opacity">
            <span className="w-7 h-7 rounded-2xl bg-primary text-primary-foreground flex items-center justify-center font-mono text-xs font-bold select-none">
              {initials(user?.email)}
            </span>
            <span className="font-mono text-xs font-medium text-muted-foreground hidden sm:inline">
              {user?.email?.split('@')[0]}
            </span>
          </button>
        </DropdownMenuTrigger>

        <DropdownMenuContent align="end" className="w-60">
          <DropdownMenuLabel className="font-normal">
            <p className="font-mono text-sm font-medium truncate">{user?.email}</p>
            <p className="font-mono text-xs text-muted-foreground">// signed_in</p>
          </DropdownMenuLabel>
          <DropdownMenuSeparator />

          {/* Settings */}
          <DropdownMenuItem onClick={() => setView('settings')}>
            <Settings className="w-4 h-4 mr-2" />
            settings
          </DropdownMenuItem>

          {/* Theme picker */}
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <Palette className="w-4 h-4 mr-2" />
              theme
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent className="w-52 p-2">
              {PALETTE_GROUPS.map((group, gi) => (
                <div key={group.label}>
                  {gi > 0 && <div className="my-1.5 border-t border-border" />}
                  <p className="px-1 pb-1 font-mono text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    {group.label}
                  </p>
                  {group.themes.map(p => (
                    <button
                      key={p.id}
                      onClick={() => setTheme(p.id)}
                      className={cn(
                        "w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md font-mono text-sm transition-colors",
                        "hover:bg-accent hover:text-accent-foreground",
                        theme === p.id && "bg-accent text-accent-foreground font-medium"
                      )}
                    >
                      <span className="relative w-5 h-5 rounded-full border border-border overflow-hidden shrink-0">
                        <span className="absolute inset-0 left-0 w-1/2" style={{ background: p.bg }} />
                        <span className="absolute inset-0 left-1/2" style={{ background: p.accent }} />
                      </span>
                      {p.name}
                      {theme === p.id && <Check className="w-3.5 h-3.5 ml-auto" />}
                    </button>
                  ))}
                </div>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>

          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={() => setView('accountDetails')}>
            account_details
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onClick={logout} className="text-destructive focus:text-destructive">
            logout
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}
