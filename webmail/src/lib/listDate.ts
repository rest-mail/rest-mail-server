/**
 * The date shown on a message-list row. Webmail keeps its terminal style
 * ("yesterday", "20_nov"); the desktop app shows the dates a native mail client
 * shows: the time today, "Yesterday", the weekday within the last week, the day
 * and month this year, and the day, month and year before that.
 */
export function formatListDate(value: string | Date, now: Date, desktop: boolean, locale?: string): string {
  const date = value instanceof Date ? value : new Date(value);
  const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const days = Math.round((startOfDay(now) - startOfDay(date)) / 86_400_000);
  if (days === 0) {
    return date.toLocaleTimeString(locale ?? [], { hour: '2-digit', minute: '2-digit' });
  }
  if (days === 1) {
    return desktop ? 'Yesterday' : 'yesterday';
  }
  if (!desktop) {
    return date.toLocaleDateString(locale ?? [], { month: 'short', day: 'numeric' }).toLowerCase().replace(' ', '_');
  }
  if (days > 1 && days < 7) {
    return date.toLocaleDateString(locale ?? [], { weekday: 'short' });
  }
  if (date.getFullYear() === now.getFullYear()) {
    return date.toLocaleDateString(locale ?? [], { day: 'numeric', month: 'short' });
  }
  return date.toLocaleDateString(locale ?? [], { day: 'numeric', month: 'short', year: 'numeric' });
}
