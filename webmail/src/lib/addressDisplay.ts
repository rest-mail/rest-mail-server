import punycode from 'punycode.js';

/** Render an addr-spec with a Unicode domain without changing its delivery value. */
export function displayAddress(address: string): string {
  // A quoted local part may contain @. It must remain byte-for-byte intact.
  const at = address.lastIndexOf('@');
  if (at <= 0) return address;
  const domain = address.slice(at + 1);
  if (!/(^|\.)xn--/i.test(domain)) return address;
  // Do not let URL parsing reinterpret a malformed address as credentials/path/etc.
  if (/[\s:/\\?#@<>[\]%]/u.test(domain)) return address;

  try {
    // The browser's IDNA parser rejects malformed A-labels before decoding them.
    const ascii = new URL(`https://${domain}`).hostname;
    const unicode = punycode.toUnicode(domain.replace(/(^|\.)(xn--[^.]+)/gi,
      (_, dot: string, label: string) => dot + label.toLowerCase()));
    if (new URL(`https://${unicode}`).hostname !== ascii) return address;
    return address.slice(0, at + 1) + unicode;
  } catch {
    // Untrusted/malformed headers must not break the message list or reader.
    return address;
  }
}
