import { beforeEach, describe, expect, it } from 'vitest';
import { tabs } from './tabs';
const message = (subject: string) => ({ to: 'friend@example.test', cc: '', bcc: '', subject });
describe('desktop draft tabs', () => {
  beforeEach(() => tabs.clear());
  it('creates a compose tab for an OS drop and updates only the receiving draft', () => {
    const first = tabs.open(message('Existing'));
    const files = [{ id: 'one', filename: 'file.txt', size: 4 }];
    tabs.attach('drop-1', files, 'sender@example.test');
    expect(tabs.get().active).toBe('drop-1');
    expect(tabs.get().tabs[1]).toMatchObject({ attachments: files, from: 'sender@example.test', dirty: true });
    tabs.attach('drop-1', [], '');
    expect(tabs.get().tabs[1].attachments).toEqual([]);
    expect(tabs.get().tabs[0]).toMatchObject({ id: first, attachments: [] });
  });
  it('retains multiple independent draft states while switching to mail', () => {
    const first = tabs.open(message('One'), 'first@example.test');
    const second = tabs.open(message('Two'), 'second@example.test');
    tabs.select('mail');
    expect(tabs.get().tabs.map(tab => [tab.initialState.subject, tab.from])).toEqual([['One', 'first@example.test'], ['Two', 'second@example.test']]);
    tabs.select(first); expect(tabs.get().active).toBe(first);
    tabs.update(second, { subject: 'Changed', dirty: true });
    expect(tabs.list()[2]).toMatchObject({ title: 'Changed', dirty: true, active: false });
  });
  it('cycles both ways including mail and chooses a neighbor on close', () => {
    const first = tabs.open(message('One')); const second = tabs.open(message('Two'));
    tabs.cycle(false); expect(tabs.get().active).toBe('mail');
    tabs.cycle(true); expect(tabs.get().active).toBe(second);
    tabs.close(second); expect(tabs.get().active).toBe(first);
    tabs.close(first); expect(tabs.get().active).toBe('mail');
    tabs.select('unknown'); tabs.close('unknown'); expect(tabs.get().tabs).toEqual([]);
  });
});
