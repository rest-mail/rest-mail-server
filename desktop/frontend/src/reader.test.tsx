import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { configureMailPlatform } from '@webmail/platform';
import { HtmlMessageBody } from '@webmail/components/mail/HtmlMessageBody';
import { TrustReceipt } from '@webmail/components/mail/signals/TrustReceipt';

const hostile = '<h1>Hello</h1><script>window.pwned=true</script><img src="https://tracker.test/pixel" onerror="window.pwned=true"><iframe src="https://evil.test"></iframe><a href="javascript:alert(1)">Bad</a>';
describe('desktop HTML reader security', () => {
  beforeEach(() => configureMailPlatform({ desktop: true }));
  it('sanitizes scripts and handlers and grants no iframe sandbox permissions', () => {
    render(<HtmlMessageBody html={hostile} />);
    const frame = screen.getByTitle('Message content');
    expect(frame.getAttribute('sandbox')).toBe('');
    const source = frame.getAttribute('srcdoc')!;
    expect(source).not.toContain('<script'); expect(source).not.toContain('onerror'); expect(source).not.toContain('<iframe'); expect(source).not.toContain('javascript:');
    expect(source).toContain("script-src 'none'");
    expect((window as unknown as { pwned?: boolean }).pwned).toBeUndefined();
  });
  it('blocks remote images by default, enables only on demand, resets per message', () => {
    const view = render(<HtmlMessageBody html={hostile} />);
    const source = () => screen.getByTitle('Message content').getAttribute('srcdoc');
    expect(source()).toContain('img-src data:');
    fireEvent.click(screen.getByTestId('reader-load-images'));
    expect(source()).toContain('img-src http: https: data:');
    view.rerender(<HtmlMessageBody html={hostile + '<p>Next message</p>'} />);
    expect(source()).toContain('img-src data:');
    expect(screen.getByTitle('Message content').getAttribute('sandbox')).toBe('');
  });
  it('preserves the existing webmail safe sizing path', () => {
    configureMailPlatform({}); render(<HtmlMessageBody html="<p>Web mail</p>" />);
    expect(screen.getByTitle('Message content').getAttribute('sandbox')).toBe('allow-same-origin allow-popups allow-popups-to-escape-sandbox');
  });
  it('uses plain sender-trust copy only on desktop', () => {
    const view = render(<TrustReceipt remoteImagesBlocked={1} />);
    expect(screen.getByTestId('reader-sender-trust').textContent).toContain('Sender trust');
    expect(screen.queryByText('// trust')).toBeNull();
    fireEvent.click(screen.getByTestId('reader-sender-trust'));
    expect(screen.getByText('1 remote image blocked')).toBeTruthy();
    configureMailPlatform({});
    view.rerender(<TrustReceipt remoteImagesBlocked={1} />);
    expect(screen.getByText('// trust')).toBeTruthy();
  });
});
