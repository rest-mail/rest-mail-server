import type { SessionBinding, SessionStatus, RuntimeEvents } from '../src/session';

export const streamURL = (url: string) => `/__desktop_mock${url}`;
export const mockFetch: typeof fetch = (input, options) => fetch(streamURL(String(input)), options);

let status: SessionStatus = { SignedIn: false, Server: '', Email: '' };
const listeners = new Map<string, Set<(data: unknown) => void>>();
export const events: RuntimeEvents = {
  on(name, handler) {
    const handlers = listeners.get(name) ?? new Set();
    handlers.add(handler); listeners.set(name, handlers);
    return () => { handlers.delete(handler); };
  },
  emit(name, data) { listeners.get(name)?.forEach(handler => handler(data)); },
};
export const session: SessionBinding = {
  async SignIn(server, email, password) {
    if (!server || !email || !password) throw new Error('Credentials required');
    status = { Server: server, Email: email, SignedIn: password !== 'totp' };
    if (status.SignedIn) events.emit('session:changed', status);
    return { NeedsTOTP: !status.SignedIn };
  },
  async SubmitTOTP(code) {
    if (code !== '123456') throw new Error('Use 123456 in the development fixture');
    status.SignedIn = true; events.emit('session:changed', status);
  },
  async ChangePassword() { return { NeedsTOTP: false, SetupRequired: status.SetupRequired }; },
  async EnrollTwoFactor() { throw new Error('Authenticator enrollment requires a real test server'); },
  async ConfirmTwoFactor() { throw new Error('Authenticator enrollment requires a real test server'); },
  async SignOut() { status = { SignedIn: false, Server: '', Email: '' }; events.emit('session:changed', status); },
  async Status() { return status; },
};
