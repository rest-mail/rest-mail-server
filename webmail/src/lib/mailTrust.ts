export type MailHeaders = Record<string, string | string[] | undefined> | null | undefined;

export type AuthOutcome =
  | 'pass'
  | 'fail'
  | 'softfail'
  | 'neutral'
  | 'none'
  | 'temperror'
  | 'permerror'
  | 'policy'
  | 'unknown';

export interface AuthCheck {
  method: string;
  result: AuthOutcome;
  identity?: string;
  domain?: string;
  reason?: string;
  raw: string;
}

export interface MailAuth {
  authservId: string | null;
  checks: AuthCheck[];
}

export type TrustState =
  | 'signed'
  | 'aligned'
  | 'spf-only'
  | 'unsigned'
  | 'forwarded'
  | 'mismatch'
  | 'unchecked';

export type TrustTone = 'ok' | 'info' | 'warn' | 'danger' | 'none';

export interface TrustVerdict {
  state: TrustState;
  tone: TrustTone;
  label: string;
  detail: string;
  signedBy: string | null;
  auth: MailAuth | null;
}

const OUTCOMES = new Set<string>([
  'pass', 'fail', 'softfail', 'neutral', 'none', 'temperror', 'permerror', 'policy',
]);

function normalizeOutcome(value: string): AuthOutcome {
  const v = value.toLowerCase();
  return OUTCOMES.has(v) ? (v as AuthOutcome) : 'unknown';
}

export function getHeader(headers: MailHeaders, name: string): string | undefined {
  if (!headers) return undefined;
  const wanted = name.toLowerCase();
  for (const [key, value] of Object.entries(headers)) {
    if (key.toLowerCase() !== wanted) continue;
    if (Array.isArray(value)) return value.filter(Boolean).join('\n');
    return value ?? undefined;
  }
  return undefined;
}

function unquote(value: string): string {
  return value.replace(/^"|"$/g, '');
}

function parseClause(clause: string): AuthCheck | null {
  const match = clause.trim().match(/^([A-Za-z0-9][\w.-]*)\s*=\s*([A-Za-z]+)\b(.*)$/);
  if (!match) return null;

  const [, method, rawResult, rest] = match;
  const propertyMatch = rest.match(/(?:^|\s)(smtp\.mailfrom|smtp\.helo|header\.d|header\.i|header\.from)=("[^"]*"|[^\s;]+)/);
  const reasonMatch = rest.match(/(?:^|\s)reason=("[^"]*"|[^\s;]+)/);

  let identity: string | undefined;
  let domain: string | undefined;
  if (propertyMatch) {
    identity = propertyMatch[1];
    const value = unquote(propertyMatch[2]);
    domain = identity === 'header.i' ? value.replace(/^@/, '') : value;
    if (identity === 'smtp.mailfrom' && value.includes('@')) {
      domain = value.slice(value.lastIndexOf('@') + 1);
    }
  }

  return {
    method: method.toLowerCase(),
    result: normalizeOutcome(rawResult),
    identity,
    domain,
    reason: reasonMatch ? unquote(reasonMatch[1]) : undefined,
    raw: clause.trim(),
  };
}

export function parseAuthResults(raw: string): MailAuth {
  const lines = raw.split(/\r?\n/).map(l => l.trim()).filter(Boolean);
  let authservId: string | null = null;
  const checks: AuthCheck[] = [];

  for (const line of lines) {
    const clauses = line.split(';');
    const head = clauses.shift()?.trim() ?? '';
    if (!authservId && head) authservId = head.replace(/\s+\d+$/, '');
    for (const clause of clauses) {
      const check = parseClause(clause);
      if (check) checks.push(check);
    }
  }

  return { authservId, checks };
}

export function authFromHeaders(headers: MailHeaders): MailAuth | null {
  const raw = getHeader(headers, 'authentication-results');
  if (!raw) return null;
  const auth = parseAuthResults(raw);
  return auth.checks.length > 0 ? auth : null;
}

function outcomeFor(auth: MailAuth, method: string): AuthOutcome | null {
  const matching = auth.checks.filter(c => c.method === method);
  if (matching.length === 0) return null;
  const passed = matching.find(c => c.result === 'pass');
  if (passed) return 'pass';
  const failed = matching.find(c => c.result === 'fail');
  if (failed) return 'fail';
  return matching[0].result;
}

function domainFor(auth: MailAuth, method: string): string | null {
  const matching = auth.checks.filter(c => c.method === method && c.domain);
  const passed = matching.find(c => c.result === 'pass');
  return (passed ?? matching[0])?.domain ?? null;
}

export function verdictFromAuth(auth: MailAuth | null): TrustVerdict {
  if (!auth || auth.checks.length === 0) {
    return {
      state: 'unchecked',
      tone: 'none',
      label: 'not checked',
      detail: 'This message carries no Authentication-Results header, so no sender checks are available.',
      signedBy: null,
      auth: null,
    };
  }

  const spf = outcomeFor(auth, 'spf');
  const dkim = outcomeFor(auth, 'dkim');
  const dmarc = outcomeFor(auth, 'dmarc');
  const arc = outcomeFor(auth, 'arc');

  const dkimDomain = domainFor(auth, 'dkim');
  const dmarcDomain = domainFor(auth, 'dmarc');
  const spfDomain = domainFor(auth, 'spf');

  if (dmarc === 'fail') {
    if (arc === 'pass') {
      return {
        state: 'forwarded',
        tone: 'warn',
        label: 'forwarded',
        detail: 'DMARC failed for the from address, but a forwarder\'s ARC chain is intact. Common for mailing lists and redirects.',
        signedBy: domainFor(auth, 'arc') ?? null,
        auth,
      };
    }
    return {
      state: 'mismatch',
      tone: 'danger',
      label: 'unverified sender',
      detail: 'DMARC failed and the sending domain does not align with the from address. Treat the from address as unverified.',
      signedBy: null,
      auth,
    };
  }

  if (dkim === 'pass') {
    return {
      state: 'signed',
      tone: 'ok',
      label: 'signed',
      detail: `The message carries a valid DKIM signature for ${dkimDomain ?? 'the sending domain'}.`,
      signedBy: dkimDomain,
      auth,
    };
  }

  if (dmarc === 'pass') {
    return {
      state: 'aligned',
      tone: 'info',
      label: 'aligned',
      detail: `DMARC passed for ${dmarcDomain ?? 'the from domain'}, but no valid DKIM signature was found. The sending server was authorised, not the message signed.`,
      signedBy: dmarcDomain,
      auth,
    };
  }

  if (spf === 'pass') {
    return {
      state: 'spf-only',
      tone: 'info',
      label: 'SPF only',
      detail: `The sending server (${spfDomain ?? 'the envelope domain'}) is authorised for the domain, but the message itself is not signed.`,
      signedBy: null,
      auth,
    };
  }

  return {
    state: 'unsigned',
    tone: 'none',
    label: 'no signature',
    detail: 'No sender signature was verified. This is normal for forwarded mail and some mailing lists, and it says nothing about the content.',
    signedBy: null,
    auth,
  };
}

export function trustFromHeaders(headers: MailHeaders): TrustVerdict {
  return verdictFromAuth(authFromHeaders(headers));
}

export function formatAge(since: string, now: number = Date.now()): string | null {
  const started = new Date(since).getTime();
  if (Number.isNaN(started)) return null;
  const minutes = Math.max(0, Math.round((now - started) / 60000));
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h`;
  const days = Math.round(hours / 24);
  if (days < 14) return `${days}d`;
  return `${Math.round(days / 7)}w`;
}

export interface DueDescription {
  text: string;
  overdue: boolean;
}

export function describeDue(due: string, now: number = Date.now()): DueDescription | null {
  const at = new Date(due).getTime();
  if (Number.isNaN(at)) return null;

  const diff = at - now;
  const abs = Math.abs(diff);
  const minutes = Math.round(abs / 60000);
  const hours = Math.round(abs / 3600000);

  if (diff < 0) {
    const unit = minutes < 60 ? `${minutes}m` : hours < 48 ? `${hours}h` : `${Math.round(hours / 24)}d`;
    return { text: `${unit} over`, overdue: true };
  }

  const target = new Date(at);
  const today = new Date(now);
  if (target.toDateString() === today.toDateString()) {
    return { text: target.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }), overdue: false };
  }

  const days = Math.round(hours / 24);
  if (days <= 1) return { text: 'tomorrow', overdue: false };
  if (days < 7) return { text: target.toLocaleDateString([], { weekday: 'short' }), overdue: false };
  return { text: target.toLocaleDateString([], { month: 'short', day: 'numeric' }), overdue: false };
}
