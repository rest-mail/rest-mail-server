import { Call, Events } from '@wailsio/runtime';
import type { NativeAttachment } from '@webmail/platform';
import type { SetupStep } from '@webmail/components/auth/AccountAccess';

export interface SessionStatus { SignedIn: boolean; Server: string; Email: string; SetupRequired?: SetupStep[] }
export interface SignInResult { NeedsTOTP: boolean; SetupRequired?: SetupStep[] }
export interface TwoFactorEnrollment { Secret: string; OTPAuthURL: string; RecoveryCodes: string[] }
export interface SessionBinding {
  SignIn(server: string, email: string, password: string): Promise<SignInResult>;
  SubmitTOTP(code: string): Promise<void>;
  ChangePassword(current: string, replacement: string): Promise<SignInResult>;
  EnrollTwoFactor(): Promise<TwoFactorEnrollment>;
  ConfirmTwoFactor(code: string): Promise<void>;
  SignOut(): Promise<void>;
  Status(): Promise<SessionStatus>;
}
const service = 'main.';
const invoke = <T,>(name: string, ...args: unknown[]) => Call.ByName(service + name, ...args) as Promise<T>;
export const session: SessionBinding = {
  SignIn: (server, email, password) => invoke('Session.SignIn', server, email, password),
  SubmitTOTP: (code) => invoke('Session.SubmitTOTP', code),
  ChangePassword: (current, replacement) => invoke('Session.ChangePassword', current, replacement),
  EnrollTwoFactor: () => invoke('Session.EnrollTwoFactor'),
  ConfirmTwoFactor: (code) => invoke('Session.ConfirmTwoFactor', code),
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
  return status.SignedIn ? { id: 0, email: status.Email, display_name: status.Email, setup_required: status.SetupRequired ?? [] } : null;
}
