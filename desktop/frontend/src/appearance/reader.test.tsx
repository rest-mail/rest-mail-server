import { act, render, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { appearance } from './engine';
import { useReaderPaper } from './reader';

it('frames HTML in light paper, updates the reading fallback, and preserves content and the security sandbox', async () => {
  localStorage.clear(); appearance.restore('test.invalid', 'reader@test.invalid');
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => new Uint8Array([1, 2, 3]).buffer }));
  const message = '<html><head><meta http-equiv="Content-Security-Policy" content="default-src none; font-src data:"></head><body><p style="font-family:serif">Sender message</p></body></html>';
  function Reader() { useReaderPaper(); return <div className="desktop-reader-content"><iframe title="Message content" sandbox="" srcDoc={message} /><iframe title="Other embedded content" /></div>; }
  const view = render(<Reader />);
  const frame = document.querySelector<HTMLIFrameElement>('iframe')!;
  await waitFor(() => expect(frame.getAttribute('srcdoc')).toContain('background: #ffffff'));
  act(() => appearance.override({ reading: 'Literata' }));
  await waitFor(() => expect(frame.getAttribute('srcdoc')).toContain('font-family: "Literata"'));
  expect(frame.getAttribute('sandbox')).toBe('');
  expect(frame.getAttribute('srcdoc')).toContain('<body><p style="font-family:serif">Sender message</p></body>');
  expect(frame.getAttribute('srcdoc')).toContain('font-src data:');
  expect(frame.getAttribute('srcdoc')).toContain('data:font/woff2;base64,AQID');
  expect(frame.getAttribute('srcdoc')?.match(/data-desktop-paper/g)).toHaveLength(1);
  expect(document.querySelectorAll('iframe')[1].getAttribute('srcdoc')).toBe(null);
  view.unmount();
  vi.unstubAllGlobals();
});
