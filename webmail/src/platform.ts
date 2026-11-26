import type { User } from './types';
import type { SetupStep } from './components/auth/AccountAccess';
import type { ComposeState } from './stores/uiStore';
import type { SSEEvent } from './lib/mailStream';

export interface NativeAttachment {
  filename: string;
  content_type: string;
  content: string;
}

export interface MailPlatform {
  desktop?: boolean;
  mailboxActive?: () => boolean;
  fetch?: typeof fetch;
  streamURL?: (url: string) => string;
  openStream?: (accountId: number, onEvent: (event: SSEEvent) => void) => () => void;
  serverHost?: () => string;
  canSendAttachments?: boolean;
  restoreSession?: () => Promise<(User & { setup_required?: SetupStep[] }) | null>;
  signOut?: () => Promise<void>;
  pickAttachments?: () => Promise<NativeAttachment[]>;
  saveAttachment?: (url: string, filename: string) => Promise<void>;
  openCompose?: (state?: ComposeState) => void;
  importThunderbird?: () => void;
  nativeNotifications?: boolean;
  onMailEvent?: (event: SSEEvent) => void;
  registerMessageAction?: (name: string, action: () => void) => () => void;
}

let platform: MailPlatform = {};

export function configureMailPlatform(value: MailPlatform) {
  platform = value;
}

export function mailPlatform(): MailPlatform {
  return platform;
}

export const mailFetch: typeof fetch = (input, init) => (platform.fetch ?? fetch)(input, init);
