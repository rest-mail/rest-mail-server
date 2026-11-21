import { Call, Events } from '@wailsio/runtime';
import type { NativeAttachment } from '@webmail/platform';

export interface SessionStatus { SignedIn: boolean; Server: string; Email: string }
export interface SignInResult { NeedsTOTP: boolean }
export interface SessionBinding {
  SignIn(server: string, email: string, password: string): Promise<SignInResult>;
  SubmitTOTP(code: string): Promise<void>;
  SignOut(): Promise<void>;
  Status(): Promise<SessionStatus>;
}
const service = 'github.com/rest-mail/rest-mail-server/desktop.';
const invoke = <T,>(name: string, ...args: unknown[]) => Call.ByName(service + name, ...args) as Promise<T>;
export const session: SessionBinding = {
  SignIn: (server, email, password) => invoke('Session.SignIn', server, email, password),
  SubmitTOTP: (code) => invoke('Session.SubmitTOTP', code),
  SignOut: () => invoke('Session.SignOut'),
  Status: () => invoke('Session.Status'),
};
export const nativeFiles = {
  pick: () => invoke<NativeAttachment[]>('Files.PickAttachments'),
  save: (filename: string, data: string) => invoke<void>('Files.SaveAttachment', filename, data),
};
export interface RuntimeEvents {
  on(name: string, handler: (data: unknown) => void): () => void;
  emit(name: string, data: unknown): void;
}
export const runtimeEvents: RuntimeEvents = {
  on: (name, handler) => Events.On(name, (event) => handler(event.data)),
  emit: (name, data) => { void Events.Emit(name, data); },
};

export function identity(status: SessionStatus) {
  return status.SignedIn ? { id: 0, email: status.Email, display_name: status.Email } : null;
}
