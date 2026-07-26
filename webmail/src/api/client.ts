import type { LoginResponse, Folder, MessageSummary, MessageDetail, Account, Pagination, Attachment } from '../types';
import { API_BASE as BASE } from './base';

// BASE is the same-origin API path (default `/api/v1`); see ./base for how it is
// resolved and why a missing/invalid VITE_API_URL no longer falls back to a
// plaintext localhost URL.

// The access token is NEVER held in JavaScript. It lives only in the httpOnly
// `restmail_access` cookie the API sets at login/refresh, which the browser
// attaches automatically to same-origin requests (credentials: 'include').
// JavaScript — and therefore any XSS — cannot read or exfiltrate it.
//
// State-changing requests must carry the double-submit CSRF token: the API also
// sets a readable `restmail_csrf` cookie, whose value we echo in the
// X-CSRF-Token header on mutating methods.

const MUTATING_METHODS = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);

/** Read the readable (non-httpOnly) CSRF token the API set at login/refresh. */
export function getCsrfToken(): string {
  const match = document.cookie.match(/(?:^|;\s*)restmail_csrf=([^;]*)/);
  return match ? decodeURIComponent(match[1]) : '';
}

export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

let onUnauthorized: (() => void) | null = null;

export function setOnUnauthorized(handler: () => void) {
  onUnauthorized = handler;
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string> || {}),
  };
  const method = (options.method || 'GET').toUpperCase();
  if (MUTATING_METHODS.has(method)) {
    headers['X-CSRF-Token'] = getCsrfToken();
  }
  // credentials: 'include' sends the httpOnly session cookie (and the CSRF
  // cookie) with the request; the token itself is never touched by JS.
  const res = await fetch(path, { ...options, headers, credentials: 'include' });
  if (!res.ok) {
    if (res.status === 401 && onUnauthorized) {
      onUnauthorized();
    }
    const body = await res.text();
    try {
      const parsed = JSON.parse(body);
      if (parsed.error) {
        throw new ApiError(res.status, parsed.error.code || 'unknown', parsed.error.message || body);
      }
    } catch (e) {
      if (e instanceof ApiError) throw e;
    }
    throw new ApiError(res.status, 'unknown', `API error ${res.status}: ${body}`);
  }
  return res.json();
}

// requestNoContent is for endpoints that reply 204 No Content (nothing to parse)
// and whose 401 is a domain error — e.g. a wrong 2FA code — rather than an
// expired session. It therefore deliberately does NOT invoke the global
// unauthorized handler, which would otherwise log the user out mid-flow.
async function requestNoContent(path: string, options: RequestInit = {}): Promise<void> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string> || {}),
  };
  const method = (options.method || 'GET').toUpperCase();
  if (MUTATING_METHODS.has(method)) {
    headers['X-CSRF-Token'] = getCsrfToken();
  }
  const res = await fetch(path, { ...options, headers, credentials: 'include' });
  if (!res.ok) {
    const body = await res.text();
    try {
      const parsed = JSON.parse(body);
      if (parsed.error) {
        throw new ApiError(res.status, parsed.error.code || 'unknown', parsed.error.message || body);
      }
    } catch (e) {
      if (e instanceof ApiError) throw e;
    }
    throw new ApiError(res.status, 'unknown', `API error ${res.status}: ${body}`);
  }
}

// Auth

/** A TOTP code or a one-time recovery code, for 2FA-gated login and disable. */
export interface SecondFactor {
  totp_code?: string;
  recovery_code?: string;
}

export async function login(email: string, password: string, second?: SecondFactor): Promise<LoginResponse> {
  // On success the API sets the httpOnly access cookie + readable CSRF cookie;
  // the response body carries only identity/expiry, never the token.
  //
  // When the account has 2FA active and no second factor was supplied, the API
  // answers 401 with error code "totp_required". We deliberately do NOT route
  // this through request(): a wrong-password or 2FA 401 during login is a
  // form-validation failure, not an expired session, so it must never fire the
  // global unauthorized handler (which would toast "session expired").
  const res = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email, password, ...second }),
  });
  if (!res.ok) {
    const body = await res.text();
    let code = 'unknown';
    let message = body;
    try {
      const parsed = JSON.parse(body);
      if (parsed.error) {
        code = parsed.error.code || 'unknown';
        message = parsed.error.message || body;
      }
    } catch { /* non-JSON error body */ }
    throw new ApiError(res.status, code, message);
  }
  return res.json();
}

// refreshSession exchanges the httpOnly refresh cookie for a fresh access cookie
// on boot (and after a 401). It returns the restored user on success, or null
// when there is no valid session. The refresh cookie is scoped to /auth, so this
// is the one call that carries it.
export async function refreshSession(): Promise<LoginResponse['data'] | null> {
  const res = await fetch(`${BASE}/auth/refresh`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'X-CSRF-Token': getCsrfToken() },
  });
  if (!res.ok) return null;
  const body = (await res.json()) as LoginResponse;
  return body.data;
}

export async function logout(): Promise<void> {
  await fetch(`${BASE}/auth/logout`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'X-CSRF-Token': getCsrfToken() },
  });
}

// ── Two-factor authentication (TOTP) ──
// The server (see two_factor.go) fully supports per-account TOTP: status →
// enroll → confirm → disable, all keyed on the caller's own session.

export interface TwoFactorStatus {
  /** True once a confirmed enrollment gates login. */
  enabled: boolean;
  /** True when an enrollment exists but has not been confirmed yet. */
  pending: boolean;
}

export interface TwoFactorEnrollment {
  /** base32 TOTP secret, for manual entry into an authenticator app. */
  secret: string;
  /** otpauth:// provisioning URI (the QR-code payload). */
  otpauth_url: string;
  /** One-time recovery codes, returned ONCE at enrollment. */
  recovery_codes: string[];
}

export async function getTwoFactorStatus(): Promise<{ data: TwoFactorStatus }> {
  return request(`${BASE}/auth/2fa`);
}

/** Begin enrollment: mints a pending TOTP secret + recovery codes. */
export async function enrollTwoFactor(): Promise<{ data: TwoFactorEnrollment }> {
  return request(`${BASE}/auth/2fa/enroll`, { method: 'POST' });
}

/** Confirm enrollment by verifying a first code; flips 2FA active (204). */
export async function confirmTwoFactor(code: string): Promise<void> {
  await requestNoContent(`${BASE}/auth/2fa/confirm`, {
    method: 'POST',
    body: JSON.stringify({ code }),
  });
}

/** Disable 2FA; requires a current TOTP code or an unused recovery code (204). */
export async function disableTwoFactor(proof: SecondFactor): Promise<void> {
  await requestNoContent(`${BASE}/auth/2fa/disable`, {
    method: 'POST',
    body: JSON.stringify(proof),
  });
}

// Accounts
export async function listAccounts(): Promise<{ data: Account[] }> {
  return request(`${BASE}/accounts`);
}

export async function testConnection(data: { address: string; password: string }): Promise<{ data: { status: string; address: string; display_name: string } }> {
  return request(`${BASE}/accounts/test-connection`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function linkAccount(data: { address: string; password: string; display_name: string }): Promise<{ data: Account }> {
  return request(`${BASE}/accounts`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

// Folders
export async function listFolders(accountId: number): Promise<{ data: Folder[] }> {
  return request(`${BASE}/accounts/${accountId}/folders`);
}

// Messages
export async function listMessages(
  accountId: number,
  folder: string,
  cursor?: string
): Promise<{ data: MessageSummary[]; pagination?: Pagination }> {
  let path = `${BASE}/accounts/${accountId}/folders/${encodeURIComponent(folder)}/messages?limit=50`;
  if (cursor) path += `&cursor=${encodeURIComponent(cursor)}`;
  return request(path);
}

export async function getMessage(msgId: number): Promise<{ data: MessageDetail }> {
  return request(`${BASE}/messages/${msgId}`);
}

export async function updateMessage(msgId: number, updates: Record<string, unknown>): Promise<void> {
  await request(`${BASE}/messages/${msgId}`, {
    method: 'PATCH',
    body: JSON.stringify(updates),
  });
}

export async function deleteMessage(msgId: number): Promise<void> {
  await request(`${BASE}/messages/${msgId}`, { method: 'DELETE' });
}

// Search
export async function searchMessages(
  accountId: number,
  query: string,
  folder?: string
): Promise<{ data: MessageSummary[] }> {
  let path = `${BASE}/accounts/${accountId}/search?q=${encodeURIComponent(query)}`;
  if (folder) path += `&folder=${encodeURIComponent(folder)}`;
  return request(path);
}

// Quota
export interface QuotaData {
  quota_bytes: number;
  quota_used_bytes: number;
  message_count: number;
  percent_used: number;
}

export async function getAccountQuota(accountId: number): Promise<{ data: QuotaData }> {
  return request(`${BASE}/accounts/${accountId}/quota`);
}

// Attachments
export async function listAttachments(messageId: number): Promise<{ data: Attachment[] }> {
  return request(`${BASE}/messages/${messageId}/attachments`);
}

export function getAttachmentUrl(attachmentId: number): string {
  return `${BASE}/attachments/${attachmentId}`;
}

// Contacts
export interface ContactSuggestion {
  id: number;
  email: string;
  name: string;
}

export async function suggestContacts(accountId: number, query: string): Promise<{ data: ContactSuggestion[] }> {
  return request(`${BASE}/accounts/${accountId}/contacts/suggest?q=${encodeURIComponent(query)}`);
}

// Send
export async function sendMessage(data: {
  from: string;
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  body_text: string;
  body_html?: string;
  in_reply_to?: string;
  calendar_event?: {
    method?: string;
    uid?: string;
    summary: string;
    description?: string;
    location?: string;
    dtstart: string;
    dtend: string;
    all_day?: boolean;
    attendees?: { name?: string; address: string; role?: string; rsvp?: boolean }[];
    sequence?: number;
  };
}): Promise<void> {
  await request(`${BASE}/messages/send`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

// Drafts
export async function createDraft(data: {
  from: string;
  to?: string[];
  cc?: string[];
  subject?: string;
  body_text?: string;
  body_html?: string;
}): Promise<{ data: MessageDetail }> {
  return request(`${BASE}/messages/draft`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function updateDraft(draftId: number, data: Record<string, unknown>): Promise<{ data: MessageDetail }> {
  return request(`${BASE}/messages/draft/${draftId}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
}

export async function sendDraft(draftId: number): Promise<void> {
  await request(`${BASE}/messages/draft/${draftId}/send`, {
    method: 'POST',
  });
}

// Calendar
export async function respondToCalendar(messageId: number, data: {
  response: string;
  from: string;
}): Promise<{ data: { status: string; response: string } }> {
  return request(`${BASE}/messages/${messageId}/calendar-reply`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function getCalendarEvents(accountId: number): Promise<{ data: {
  uid: string;
  method: string;
  status: string;
  summary: string;
  sequence: number;
  is_cancelled: boolean;
  message_id?: number;
  versions: number;
}[] }> {
  return request(`${BASE}/accounts/${accountId}/calendar-events`);
}

// Threads
export async function getThread(accountId: number, threadId: string): Promise<{ data: MessageSummary[] }> {
  return request(`${BASE}/accounts/${accountId}/threads/${encodeURIComponent(threadId)}`);
}

// Account management
export async function deleteAccount(accountId: number): Promise<void> {
  await request(`${BASE}/accounts/${accountId}`, { method: 'DELETE' });
}

// Vacation
export interface VacationData {
  id: number;
  mailbox_id: number;
  enabled: boolean;
  subject: string;
  body: string;
  start_date?: string;
  end_date?: string;
}

export async function getVacation(accountId: number): Promise<{ data: VacationData | null }> {
  return request(`${BASE}/accounts/${accountId}/vacation`);
}

export async function setVacation(accountId: number, data: {
  enabled: boolean;
  subject: string;
  body: string;
  start_date?: string;
  end_date?: string;
}): Promise<{ data: VacationData }> {
  return request(`${BASE}/accounts/${accountId}/vacation`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
}

export async function disableVacation(accountId: number): Promise<void> {
  await request(`${BASE}/accounts/${accountId}/vacation`, { method: 'DELETE' });
}

// Quarantine
export interface QuarantineItem {
  id: number;
  mailbox_id: number;
  sender: string;
  subject: string;
  body_preview: string;
  quarantine_reason: string;
  received_at: string;
  spam_score?: number;
}

export async function listQuarantine(accountId: number): Promise<{ data: QuarantineItem[] }> {
  return request(`${BASE}/accounts/${accountId}/quarantine`);
}

export async function releaseQuarantine(accountId: number, messageId: number): Promise<void> {
  await request(`${BASE}/accounts/${accountId}/quarantine/${messageId}/release`, { method: 'POST' });
}

export async function deleteQuarantine(accountId: number, messageId: number): Promise<void> {
  await request(`${BASE}/accounts/${accountId}/quarantine/${messageId}`, { method: 'DELETE' });
}

// ── Admin: TLS Reports ──

export interface TLSReport {
  id: number;
  domain_id: number;
  reporting_org: string;
  start_date: string;
  end_date: string;
  policy_type: string;
  policy_domain: string;
  total_successful: number;
  total_failure: number;
  failure_details?: unknown;
  received_at: string;
}

export async function listTLSReports(params?: {
  domain_id?: number;
  limit?: number;
  offset?: number;
}): Promise<{ data: TLSReport[]; pagination: Pagination }> {
  const qs = new URLSearchParams();
  if (params?.domain_id) qs.set('domain_id', String(params.domain_id));
  if (params?.limit) qs.set('limit', String(params.limit));
  if (params?.offset) qs.set('offset', String(params.offset));
  return request(`${BASE}/admin/tls-reports?${qs}`);
}

// ── Admin: Pipelines ──

export interface PipelineData {
  id: number;
  domain_id: number;
  direction: string;
  filters: FilterConfig[];
  active: boolean;
  created_at: string;
  updated_at: string;
}

export interface FilterConfig {
  name: string;
  enabled: boolean;
  config?: Record<string, unknown>;
}

export interface PipelineTestResult {
  action: string;
  logs: { filter: string; action: string; message: string; duration_ms: number }[];
  email?: unknown;
}

export async function listPipelines(): Promise<{ data: PipelineData[] }> {
  return request(`${BASE}/admin/pipelines`);
}

export async function createPipeline(data: {
  domain_id: number;
  direction: string;
  filters: FilterConfig[];
  active: boolean;
}): Promise<{ data: PipelineData }> {
  return request(`${BASE}/admin/pipelines`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function updatePipeline(id: number, data: Partial<{
  filters: FilterConfig[];
  active: boolean;
}>): Promise<{ data: PipelineData }> {
  return request(`${BASE}/admin/pipelines/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(data),
  });
}

export async function deletePipeline(id: number): Promise<void> {
  await request(`${BASE}/admin/pipelines/${id}`, { method: 'DELETE' });
}

export async function testPipeline(pipelineId: number, email: Record<string, unknown>): Promise<{ data: PipelineTestResult }> {
  return request(`${BASE}/admin/pipelines/test`, {
    method: 'POST',
    body: JSON.stringify({ pipeline_id: pipelineId, email }),
  });
}

export async function testFilter(filterName: string, config: Record<string, unknown>, email: Record<string, unknown>): Promise<{ data: PipelineTestResult }> {
  return request(`${BASE}/admin/pipelines/test-filter`, {
    method: 'POST',
    body: JSON.stringify({ filter_name: filterName, config, email }),
  });
}

// Folder management
export async function createFolder(accountId: number, name: string): Promise<{ data: Folder }> {
  return request(`${BASE}/accounts/${accountId}/folders`, {
    method: 'POST',
    body: JSON.stringify({ name }),
  });
}

export async function renameFolder(accountId: number, oldName: string, newName: string): Promise<void> {
  await request(`${BASE}/accounts/${accountId}/folders/${encodeURIComponent(oldName)}`, {
    method: 'PATCH',
    body: JSON.stringify({ name: newName }),
  });
}

export async function deleteFolder(accountId: number, folderName: string): Promise<void> {
  await request(`${BASE}/accounts/${accountId}/folders/${encodeURIComponent(folderName)}`, {
    method: 'DELETE',
  });
}
