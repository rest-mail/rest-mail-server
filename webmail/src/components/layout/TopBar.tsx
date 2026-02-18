import { useState, useCallback, useRef, useEffect } from 'react';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore, type Theme } from '@/stores/uiStore';
import { useMailStore } from '@/stores/mailStore';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
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
import { Separator } from '@/components/ui/separator';
import { Search, X, Loader2, Check, PenSquare, RefreshCw, ChevronDown, Palette, Settings } from 'lucide-react';
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
];

export function TopBar() {
  const { user, logout } = useAuthStore();
  const { startCompose, theme, setTheme, setView } = useUIStore();
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
    <>
      <div className="flex items-center justify-between gap-2 px-4 py-2 bg-background">
        {/* Left: actions */}
        <div className="flex items-center gap-2 shrink-0">
          <Button size="sm" onClick={() => startCompose()}>
            <PenSquare className="w-4 h-4 mr-1" />
            Compose
          </Button>
          <Button variant="outline" size="sm" className="hidden sm:inline-flex" onClick={() => refresh()}>
            <RefreshCw className="w-4 h-4 mr-1" />
            Get Mail
          </Button>
        </div>

        {/* Center: search */}
        <div className="flex-1 max-w-md min-w-0 hidden sm:block">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              ref={inputRef}
              type="text"
              placeholder="Search messages..."
              value={inputValue}
              onChange={handleInputChange}
              onKeyDown={e => e.key === 'Escape' && handleClearSearch()}
              className="pl-9 pr-9 h-8"
            />
            {isSearching
              ? <Loader2 className="absolute right-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground animate-spin" />
              : inputValue
                ? <button onClick={handleClearSearch} className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground">
                    <X className="h-4 w-4" />
                  </button>
                : null}
          </div>
        </div>

        {/* Right: user menu — avatar + chevron trigger */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="shrink-0 flex items-center gap-1.5 px-2">
              <span className="w-7 h-7 rounded-full bg-primary text-primary-foreground flex items-center justify-center text-xs font-semibold select-none">
                {initials(user?.email)}
              </span>
              <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
            </Button>
          </DropdownMenuTrigger>

          <DropdownMenuContent align="end" className="w-60">
            <DropdownMenuLabel className="font-normal">
              <p className="text-sm font-medium truncate">{user?.email}</p>
              <p className="text-xs text-muted-foreground">Signed in</p>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />

            {/* Settings */}
            <DropdownMenuItem onClick={() => setView('settings')}>
              <Settings className="w-4 h-4 mr-2" />
              Settings
            </DropdownMenuItem>

            {/* Theme picker */}
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <Palette className="w-4 h-4 mr-2" />
                Theme
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent className="w-52 p-2">
                {PALETTE_GROUPS.map((group, gi) => (
                  <div key={group.label}>
                    {gi > 0 && <div className="my-1.5 border-t border-border" />}
                    <p className="px-1 pb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                      {group.label}
                    </p>
                    {group.themes.map(p => (
                      <button
                        key={p.id}
                        onClick={() => setTheme(p.id)}
                        className={cn(
                          "w-full flex items-center gap-2.5 px-2 py-1.5 rounded-md text-sm transition-colors",
                          "hover:bg-accent hover:text-accent-foreground",
                          theme === p.id && "bg-accent text-accent-foreground font-medium"
                        )}
                      >
                        {/* Swatch: split circle showing bg + accent */}
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
              Account Details
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={logout} className="text-destructive focus:text-destructive">
              Logout
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <Separator />
    </>
  );
}
