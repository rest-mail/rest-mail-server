import type { LoginResponse, Folder, MessageSummary, MessageDetail, Account, Pagination, Attachment } from '../types';

const BASE = '/api/v1';

let accessToken = '';

export function setToken(token: string) {
  accessToken = token;
}

export function getToken() {
  return accessToken;
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
  if (accessToken) {
    headers['Authorization'] = `Bearer ${accessToken}`;
  }
  const res = await fetch(path, { ...options, headers });
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

// Auth
export async function login(email: string, password: string): Promise<LoginResponse> {
  return request<LoginResponse>(`${BASE}/auth/login`, {
    method: 'POST',
    body: JSON.stringify({ email, password }),
  });
}

export async function logout(): Promise<void> {
  await fetch(`${BASE}/auth/logout`, { method: 'POST', credentials: 'include' });
  accessToken = '';
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
