import { useCallback, useEffect, useReducer, useState } from 'react';
import { toast } from 'sonner';
import { createAppPassword, listAppPasswords, revokeAppPassword } from '@/api/client';
import { useAuthStore } from '@/stores/authStore';
import {
  appPasswordRefusal,
  appPasswordsReducer,
  initialAppPasswords,
  type AppPasswordRefusal,
} from '@/lib/appPasswords';
import { AppPasswordsPanel } from '@/components/settings/AppPasswordsPanel';

/**
 * AppPasswordsView manages the signed-in account's own app passwords against
 * /accounts/{id}/credentials. The API holds the session rules: making one
 * needs a recent two-factor sign-in and the current password again; when it
 * asks for a new sign-in, this offers one rather than working around it.
 */
export function AppPasswordsView() {
  const accountId = useAuthStore(s => s.user?.id);
  const logout = useAuthStore(s => s.logout);
  const [state, dispatch] = useReducer(appPasswordsReducer, initialAppPasswords);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<AppPasswordRefusal | null>(null);

  const refresh = useCallback(async () => {
    if (!accountId) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const { data } = await listAppPasswords(accountId);
      dispatch({ type: 'loaded', items: data ?? [] });
    } catch (e) {
      setError(appPasswordRefusal(e));
    } finally {
      setLoading(false);
    }
  }, [accountId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  async function create(name: string, password: string) {
    if (!accountId) return;
    setBusy(true);
    setError(null);
    try {
      const { data } = await createAppPassword(accountId, { name, password });
      dispatch({ type: 'created', created: data });
    } catch (e) {
      setError(appPasswordRefusal(e));
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: number) {
    if (!accountId) return;
    setBusy(true);
    setError(null);
    try {
      await revokeAppPassword(accountId, id);
      dispatch({ type: 'revoked', id });
      toast.success('App password revoked');
    } catch (e) {
      setError(appPasswordRefusal(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <AppPasswordsPanel
      items={state.items}
      loading={loading}
      created={state.created}
      confirmingRevoke={state.confirmingRevoke}
      busy={busy}
      error={error}
      onCreate={create}
      onDismissCreated={() => dispatch({ type: 'dismissed' })}
      onConfirmRevoke={id => dispatch({ type: 'confirmRevoke', id })}
      onRevoke={revoke}
      onSignInAgain={logout}
    />
  );
}
