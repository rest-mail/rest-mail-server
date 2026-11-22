import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createMailNotificationBatch } from './notifications';

describe('mail notification coalescing', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());
  const message = (id: number) => ({ id, sender: `sender-${id}`, subject: `subject-${id}` });
  it('posts one batch at a fixed two-second boundary, not a sliding debounce', () => {
    const post = vi.fn();
    const batch = createMailNotificationBatch(post, () => false);
    batch.add(message(1));
    vi.advanceTimersByTime(1000); batch.add(message(2));
    vi.advanceTimersByTime(999); batch.add(message(3));
    expect(post).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(post).toHaveBeenCalledExactlyOnceWith([message(1), message(2), message(3)]);
  });
  it('counts duplicates once across batches and keeps separate windows separate', () => {
    const post = vi.fn();
    const batch = createMailNotificationBatch(post, () => false);
    batch.add(message(1)); batch.add(message(1));
    vi.advanceTimersByTime(2000);
    batch.add(message(1)); batch.add(message(2));
    vi.advanceTimersByTime(2000);
    expect(post.mock.calls).toEqual([[[message(1)]], [[message(2)]]]);
  });
  it('never counts a message visible at arrival or when the window expires', () => {
    let visible = 1;
    const post = vi.fn();
    const batch = createMailNotificationBatch(post, id => id === visible);
    batch.add(message(1)); batch.add(message(2)); batch.add(message(3));
    visible = 2;
    vi.advanceTimersByTime(2000);
    expect(post).toHaveBeenCalledExactlyOnceWith([message(3)]);
  });
  it('cancels pending work on logout, account change, or disposal and resets deduplication', () => {
    const post = vi.fn();
    const batch = createMailNotificationBatch(post, () => false);
    batch.add(message(1)); batch.clear();
    vi.advanceTimersByTime(2000); expect(post).not.toHaveBeenCalled();
    batch.add(message(1)); vi.advanceTimersByTime(2000);
    expect(post).toHaveBeenCalledExactlyOnceWith([message(1)]);
  });
});
