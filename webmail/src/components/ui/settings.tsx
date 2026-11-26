import { useId, type ReactNode } from 'react';
import { Button } from './button';

export function SettingsNavButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <Button type="button" size="sm" variant="ghost" aria-pressed={active} onClick={onClick}
      className="rounded-sm border-b-2 border-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground aria-pressed:border-primary aria-pressed:text-foreground">
      {children}
    </Button>
  );
}

export function SettingsPage({ title, subtitle, navigation, children }: {
  title: string;
  subtitle?: string;
  navigation?: ReactNode;
  children: ReactNode;
}) {
  const titleId = useId();
  return (
    <section data-settings-surface aria-labelledby={titleId} className="@container flex h-full min-h-0 flex-col bg-background text-foreground">
      <header className="shrink-0 border-b border-border/55 bg-muted/35">
        <div className="mx-auto max-w-5xl px-5 py-5 @lg:px-8">
          <h2 id={titleId} className="text-2xl font-semibold tracking-tight">{title}</h2>
          {subtitle && <p className="mt-1 break-all text-sm text-muted-foreground">{subtitle}</p>}
          {navigation && <nav aria-label={title} className="mt-4 flex flex-wrap gap-1.5">{navigation}</nav>}
        </div>
      </header>
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto max-w-5xl divide-y divide-border/55 px-5 @lg:px-8">{children}</div>
      </div>
    </section>
  );
}

export function SettingsSection({ title, description, children }: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  const titleId = useId();
  return (
    <section data-settings-surface aria-labelledby={titleId} className="grid gap-4 py-5 @2xl:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] @2xl:gap-8 [&_button]:rounded-sm [&_input:not([type=checkbox])]:rounded-sm [&_textarea]:rounded-sm">
      <header>
        <h3 id={titleId} className="text-base font-semibold tracking-tight">{title}</h3>
        {description && <p className="mt-1 text-sm leading-relaxed text-muted-foreground">{description}</p>}
      </header>
      <div className="min-w-0 space-y-3 text-sm">{children}</div>
    </section>
  );
}

export function SettingsField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="flex min-w-0 flex-col gap-1.5 text-sm font-medium">
      <span>{label}</span>
      {children}
    </label>
  );
}

export function SettingsActions({ children }: { children: ReactNode }) {
  return <div className="flex flex-wrap items-center gap-2">{children}</div>;
}

export function SettingsStatus({ error, pending }: { error: string; pending?: string }) {
  if (error) return <p role="alert" className="text-sm text-destructive">{error}</p>;
  return pending ? <p role="status" className="text-sm text-muted-foreground">{pending}</p> : null;
}
