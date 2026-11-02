import type { components } from '../api/types';

export type SealState = components['schemas']['MessageSeal']['state'] | 'checking' | 'unavailable';

export const SEAL_LABELS: Record<SealState, string> = {
  intact: 'Message unchanged since this server accepted it',
  mismatch: 'Message changed since this server accepted it',
  not_sealed: 'Not sealed',
  checking: 'Checking receipt seal',
  unavailable: 'Receipt seal could not be checked',
};
