import { useEffect, useState } from 'react';
import { Call } from '@wailsio/runtime';

export interface Release { version: string; commit: string; builtAt: string }

export const loadRelease = () => Call.ByName('main.AppInfo.Release') as Promise<Release>;

/** "22 Nov 2026" for a build time, or the raw value when it is not a date. */
export function formatBuilt(value: string, locale?: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { day: 'numeric', month: 'short', year: 'numeric' }).format(date);
}

function useRelease(load: () => Promise<Release>) {
  const [release, setRelease] = useState<Release | null>(null);
  useEffect(() => {
    let live = true;
    load().then(value => { if (live) setRelease(value); }).catch(() => { if (live) setRelease(null); });
    return () => { live = false; };
  }, [load]);
  return release;
}

export function SignInVersion() {
  const release = useRelease(loadRelease);
  return release?.version ? <small className="desktop-signin-version">Version {release.version}</small> : null;
}

/** The running build: its semantic version, short commit and build date. */
export function AboutRelease({ load = loadRelease }: { load?: () => Promise<Release> }) {
  const release = useRelease(load);
  if (!release) return null;
  const commit = release.commit.replace(/-dirty$/, '');
  const dirty = release.commit.endsWith('-dirty');
  return (
    <div className="about-release" data-testid="about-release">
      <strong>REST Mail {release.version}</strong>
      <span title={release.commit}>Build {commit.slice(0, 7)}{dirty ? ' (modified)' : ''}</span>
      <span>{formatBuilt(release.builtAt)}</span>
    </div>
  );
}
