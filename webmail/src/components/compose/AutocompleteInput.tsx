import { useState, useRef, useEffect, useCallback } from 'react';
import { useMailStore } from '@/stores/mailStore';
import { suggestContacts, type ContactSuggestion } from '@/api/client';
import { cn } from '@/lib/utils';

interface AutocompleteInputProps {
  value: string[];
  onChange: (emails: string[]) => void;
  placeholder?: string;
}

export function AutocompleteInput({ value, onChange, placeholder }: AutocompleteInputProps) {
  const { activeAccountId } = useMailStore();
  const [inputValue, setInputValue] = useState('');
  const [suggestions, setSuggestions] = useState<ContactSuggestion[]>([]);
  const [showDropdown, setShowDropdown] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const [loading, setLoading] = useState(false);
  const [hasSearched, setHasSearched] = useState(false);

  const inputRef = useRef<HTMLInputElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fetchSuggestions = useCallback(async (query: string) => {
    if (!activeAccountId || query.trim().length === 0) {
      setSuggestions([]);
      setShowDropdown(false);
      setHasSearched(false);
      return;
    }

    setLoading(true);
    try {
      const resp = await suggestContacts(activeAccountId, query.trim());
      const existing = new Set(value.map(e => e.toLowerCase()));
      const filtered = resp.data.filter(s => !existing.has(s.email.toLowerCase()));
      setSuggestions(filtered);
      setHasSearched(true);
      setShowDropdown(true);
      setHighlightedIndex(-1);
    } catch {
      setSuggestions([]);
      setHasSearched(true);
      setShowDropdown(true);
    } finally {
      setLoading(false);
    }
  }, [activeAccountId, value]);

  const debouncedFetch = useCallback((query: string) => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
    }
    debounceRef.current = setTimeout(() => {
      fetchSuggestions(query);
    }, 200);
  }, [fetchSuggestions]);

  useEffect(() => {
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
      }
    };
  }, []);

  // Close dropdown on click outside
  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setShowDropdown(false);
      }
    }
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  function addRecipient(email: string) {
    const trimmed = email.trim();
    if (!trimmed) return;
    // Avoid duplicates
    if (value.some(v => v.toLowerCase() === trimmed.toLowerCase())) return;
    onChange([...value, trimmed]);
    setInputValue('');
    setSuggestions([]);
    setShowDropdown(false);
    setHasSearched(false);
    inputRef.current?.focus();
  }

  function selectSuggestion(suggestion: ContactSuggestion) {
    addRecipient(suggestion.email);
  }

  function removeRecipient(index: number) {
    const next = value.filter((_, i) => i !== index);
    onChange(next);
  }

  function handleInputChange(e: React.ChangeEvent<HTMLInputElement>) {
    const val = e.target.value;

    // Check if user typed a comma or semicolon to commit the current entry
    if (val.endsWith(',') || val.endsWith(';')) {
      const entry = val.slice(0, -1).trim();
      if (entry) {
        addRecipient(entry);
      }
      return;
    }

    setInputValue(val);
    debouncedFetch(val);
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Escape') {
      setShowDropdown(false);
      return;
    }

    if (e.key === 'Backspace' && inputValue === '' && value.length > 0) {
      removeRecipient(value.length - 1);
      return;
    }

    if (e.key === 'Enter') {
      e.preventDefault();
      if (highlightedIndex >= 0 && highlightedIndex < suggestions.length) {
        selectSuggestion(suggestions[highlightedIndex]);
      } else if (inputValue.trim()) {
        addRecipient(inputValue);
      }
      return;
    }

    if (e.key === 'Tab' && inputValue.trim()) {
      e.preventDefault();
      if (highlightedIndex >= 0 && highlightedIndex < suggestions.length) {
        selectSuggestion(suggestions[highlightedIndex]);
      } else {
        addRecipient(inputValue);
      }
      return;
    }

    if (e.key === 'ArrowDown' && showDropdown) {
      e.preventDefault();
      setHighlightedIndex(prev =>
        prev < suggestions.length - 1 ? prev + 1 : prev
      );
      return;
    }

    if (e.key === 'ArrowUp' && showDropdown) {
      e.preventDefault();
      setHighlightedIndex(prev => (prev > 0 ? prev - 1 : -1));
      return;
    }
  }

  function formatSuggestion(s: ContactSuggestion): string {
    if (s.name) {
      return `${s.name} <${s.email}>`;
    }
    return s.email;
  }

  return (
    <div ref={containerRef} className="relative flex-1">
      <div
        className={cn(
          'flex flex-wrap items-center gap-1 min-h-[36px] rounded-md border border-input bg-transparent px-2 py-1 text-sm',
          'focus-within:border-ring focus-within:ring-ring/50 focus-within:ring-[3px]',
          'dark:bg-input/30'
        )}
        onClick={() => inputRef.current?.focus()}
      >
        {value.map((email, i) => (
          <span
            key={`${email}-${i}`}
            className="inline-flex items-center gap-1 rounded-md bg-secondary text-secondary-foreground px-2 py-0.5 text-xs font-medium max-w-[240px]"
          >
            <span className="truncate">{email}</span>
            <button
              type="button"
              className="ml-0.5 text-muted-foreground hover:text-foreground shrink-0"
              onClick={(e) => {
                e.stopPropagation();
                removeRecipient(i);
              }}
              aria-label={`Remove ${email}`}
            >
              {"\u2715"}
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          type="text"
          value={inputValue}
          onChange={handleInputChange}
          onKeyDown={handleKeyDown}
          onFocus={() => {
            if (inputValue.trim() && hasSearched) {
              setShowDropdown(true);
            }
          }}
          onBlur={() => {
            // Delay hiding dropdown so click events on suggestions fire first
            setTimeout(() => {
              if (inputValue.trim()) {
                addRecipient(inputValue);
              }
            }, 200);
          }}
          placeholder={value.length === 0 ? placeholder : ''}
          className="flex-1 min-w-[120px] bg-transparent outline-none text-sm placeholder:text-muted-foreground h-7"
        />
      </div>

      {showDropdown && (
        <div className="absolute z-50 top-full left-0 right-0 mt-1 rounded-md border border-input bg-popover text-popover-foreground shadow-md max-h-[200px] overflow-y-auto">
          {loading ? (
            <div className="px-3 py-2 text-sm text-muted-foreground">
              Searching...
            </div>
          ) : suggestions.length > 0 ? (
            suggestions.map((s, i) => (
              <button
                key={s.id}
                type="button"
                className={cn(
                  'w-full text-left px-3 py-2 text-sm cursor-pointer hover:bg-accent hover:text-accent-foreground',
                  i === highlightedIndex && 'bg-accent text-accent-foreground'
                )}
                onMouseDown={(e) => {
                  e.preventDefault();
                  selectSuggestion(s);
                }}
                onMouseEnter={() => setHighlightedIndex(i)}
              >
                {formatSuggestion(s)}
              </button>
            ))
          ) : hasSearched ? (
            <div className="px-3 py-2 text-sm text-muted-foreground">
              No suggestions
            </div>
          ) : null}
        </div>
      )}
    </div>
  );
}
