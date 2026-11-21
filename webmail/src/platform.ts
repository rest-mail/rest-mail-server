import type { User } from './types';

export interface NativeAttachment {
  filename: string;
  content_type: string;
  content: string;
}

export interface MailPlatform {
  desktop?: boolean;
  fetch?: typeof fetch;
  streamURL?: (url: string) => string;
  restoreSession?: () => Promise<User | null>;
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
