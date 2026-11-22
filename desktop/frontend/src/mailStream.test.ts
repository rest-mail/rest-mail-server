import { describe, expect, it, vi } from 'vitest';
vi.mock('@wailsio/runtime', () => ({ Call: { ByName: vi.fn() }, Events: { On: vi.fn(), Emit: vi.fn() } }));
import { createNativeMailStream } from './mailStream';

describe('native mail streams', () => {
  it('subscribes before starting, isolates subscriptions, and stops after a pending start', async () => {
    const listeners = new Set<(data: unknown) => void>();
    const events = { on: (_name: string, listener: (data: unknown) => void) => { listeners.add(listener); return () => { listeners.delete(listener); }; }, emit: vi.fn() };
    let finish!: () => void;
    const start = vi.fn((_account: number, id: string) => {
      for (const listener of listeners) listener({ subscription: id, event: { type: 'stream_ready', data: {} } });
      return new Promise<void>(resolve => { finish = resolve; });
    });
    const stop = vi.fn().mockResolvedValue(undefined);
    const receive = vi.fn();
    const close = createNativeMailStream(events, start, stop)(7, receive);
    expect(receive).toHaveBeenCalledExactlyOnceWith({ type: 'stream_ready', data: {} });
    for (const listener of listeners) listener({ subscription: 'another', event: { type: 'new_message', data: {} } });
    expect(receive).toHaveBeenCalledTimes(1);
    close();
    expect(listeners.size).toBe(0);
    expect(stop).not.toHaveBeenCalled();
    finish();
    await vi.waitFor(() => expect(stop).toHaveBeenCalledExactlyOnceWith(start.mock.calls[0][1]));
  });
});
