import type { LoginResponse, Folder, MessageSummary, MessageDetail, Account, Pagination } from '../types';

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

// Send
export async function sendMessage(data: {
  from: string;
  to: string[];
  cc?: string[];
  bcc?: string[];
  subject: string;
  body_text: string;
  body_html?: string;
}): Promise<void> {
  await request(`${BASE}/messages/send`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
}
