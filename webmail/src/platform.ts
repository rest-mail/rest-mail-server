import type { User } from './types';
import type { SetupStep } from './components/auth/AccountAccess';

export interface NativeAttachment {
  filename: string;
  content_type: string;
  content: string;
}

export interface MailPlatform {
  desktop?: boolean;
  fetch?: typeof fetch;
  streamURL?: (url: string) => string;
  serverHost?: () => string;
  canSendAttachments?: boolean;
  restoreSession?: () => Promise<(User & { setup_required?: SetupStep[] }) | null>;
  signOut?: () => Promise<void>;
  pickAttachments?: () => Promise<NativeAttachment[]>;
  saveAttachment?: (url: string, filename: string) => Promise<void>;
}

let platform: MailPlatform = {};

export function configureMailPlatform(value: MailPlatform) {
  platform = value;
}

export function mailPlatform(): MailPlatform {
  return platform;
}

export const mailFetch: typeof fetch = (input, init) => (platform.fetch ?? fetch)(input, init);
