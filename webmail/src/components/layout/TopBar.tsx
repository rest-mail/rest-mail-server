import { useState, useCallback, useRef, useEffect } from 'react';
import { useAuthStore } from '@/stores/authStore';
import { useUIStore } from '@/stores/uiStore';
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
import { Search, X, Loader2, Sun, Moon, Check, PenSquare, RefreshCw } from 'lucide-react';

export function TopBar() {
  const { user, logout } = useAuthStore();
  const { startCompose, theme, setTheme, setView } = useUIStore();
  const { refresh, searchMessages, clearSearch, isSearching, searchQuery } = useMailStore();

  const [inputValue, setInputValue] = useState('');
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Sync input value when search is cleared externally
  useEffect(() => {
    if (!searchQuery && inputValue) {
      setInputValue('');
    }
  }, [searchQuery]); // eslint-disable-line react-hooks/exhaustive-deps

  const debouncedSearch = useCallback((value: string) => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
    }
    debounceRef.current = setTimeout(() => {
      if (value.trim()) {
        searchMessages(value);
      } else {
        clearSearch();
      }
    }, 300);
  }, [searchMessages, clearSearch]);

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const value = e.target.value;
    setInputValue(value);
    debouncedSearch(value);
  };

  const handleClearSearch = () => {
    setInputValue('');
    clearSearch();
    inputRef.current?.focus();
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      handleClearSearch();
      inputRef.current?.blur();
    }
  };

  return (
    <>
      <div className="flex items-center justify-between gap-2 px-4 py-2 bg-background">
        {/* Left: action buttons */}
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

        {/* Center: search bar */}
        <div className="flex-1 max-w-md min-w-0 hidden sm:block">
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
            <Input
              ref={inputRef}
              type="text"
              placeholder="Search messages..."
              value={inputValue}
              onChange={handleInputChange}
              onKeyDown={handleKeyDown}
              className="pl-9 pr-9 h-8"
            />
            {isSearching ? (
              <Loader2 className="absolute right-2.5 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground animate-spin" />
            ) : inputValue ? (
              <button
                onClick={handleClearSearch}
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            ) : null}
          </div>
        </div>

        {/* Right: user menu */}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="sm" className="shrink-0 max-w-[200px] truncate">
              {user?.email}
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel>My Account</DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={() => setView('accountDetails')}>
              View Account Details
            </DropdownMenuItem>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>Theme</DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                <DropdownMenuItem onClick={() => setTheme('dawn')}>
                  <Sun className="w-4 h-4 mr-2" />
                  Dawn
                  {theme === 'dawn' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme('linen')}>
                  <Sun className="w-4 h-4 mr-2" />
                  Linen
                  {theme === 'linen' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme('slate')}>
                  <Moon className="w-4 h-4 mr-2" />
                  Slate
                  {theme === 'slate' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme('dusk')}>
                  <Moon className="w-4 h-4 mr-2" />
                  Dusk
                  {theme === 'dusk' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme('midnight')}>
                  <Moon className="w-4 h-4 mr-2" />
                  Midnight
                  {theme === 'midnight' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => setTheme('forest')}>
                  <Moon className="w-4 h-4 mr-2" />
                  Forest
                  {theme === 'forest' && <Check className="w-4 h-4 ml-auto" />}
                </DropdownMenuItem>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={logout} className="text-destructive">
              Logout
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <Separator />
    </>
  );
}
