import type { AppPassword, CreatedAppPassword } from '@/api/client';

// The app passwords section's state. A new app password's secret is held in
// `created` only until it is dismissed, so it is shown exactly once: the list
// keeps the entry, never the secret.
export interface AppPasswordsState {
  items: AppPassword[];
  created: CreatedAppPassword | null;
  confirmingRevoke: number | null;
}

export type AppPasswordsAction =
  | { type: 'loaded'; items: AppPassword[] }
  | { type: 'created'; created: CreatedAppPassword }
  | { type: 'dismissed' }
  | { type: 'confirmRevoke'; id: number | null }
  | { type: 'revoked'; id: number };

export const initialAppPasswords: AppPasswordsState = { items: [], created: null, confirmingRevoke: null };

export function appPasswordsReducer(state: AppPasswordsState, action: AppPasswordsAction): AppPasswordsState {
  switch (action.type) {
    case 'loaded':
      return { ...state, items: action.items };
    case 'created': {
      const { id, name, kind, public_id, created_at, last_used_at, expires_at } = action.created;
      const entry: AppPassword = { id, name, kind, public_id, created_at, last_used_at, expires_at };
      return { ...state, created: action.created, items: [entry, ...state.items.filter(i => i.id !== entry.id)] };
    }
    case 'dismissed':
      return { ...state, created: null };
    case 'confirmRevoke':
      return { ...state, confirmingRevoke: action.id };
    case 'revoked':
      return {
        ...state,
        items: state.items.filter(i => i.id !== action.id),
        created: state.created?.id === action.id ? null : state.created,
        confirmingRevoke: null,
      };
  }
}

// AppPasswordRefusal is what the section says when the API refuses. `signIn`
// is set when only a new sign-in with a second factor can fix it.
export interface AppPasswordRefusal {
  message: string;
  signIn: boolean;
}

export function appPasswordRefusal(e: unknown): AppPasswordRefusal {
  const code = typeof e === 'object' && e !== null && 'code' in e ? String((e as { code: unknown }).code) : '';
  switch (code) {
    case 'two_factor_required':
      return {
        message: 'Making an app password needs two-factor authentication. Turn it on above, then sign in again with your code.',
        signIn: true,
      };
    case 'recent_two_factor_required':
      return {
        message: 'For your safety, sign in again with your authenticator code, then make the app password within 15 minutes.',
        signIn: true,
      };
    case 'invalid_password':
      return { message: 'That password is not right: enter the password you sign in with.', signIn: false };
    case 'reauth_required':
      return { message: 'Enter your current password to make an app password.', signIn: false };
    case 'rate_limited':
      return { message: 'Too many attempts. Wait a few minutes and try again.', signIn: false };
  }
  return { message: e instanceof Error ? e.message : 'Something went wrong', signIn: false };
}

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' });

export function formatCredentialDate(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : dateFormat.format(d);
}
