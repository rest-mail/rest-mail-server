import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import DOMPurify from 'dompurify';
import { ImageOff } from 'lucide-react';
import { useUIStore } from '@/stores/uiStore';

/**
 * HtmlMessageBody renders untrusted email HTML with defence-in-depth against
 * the classic webmail attacks (OSI-16):
 *
 *  1. SANITISE — DOMPurify with a strict tag allowlist. `style` and `class`
 *     are intentionally NOT allowed, which removes the CSS exfiltration surface
 *     (`background:url(attacker)`, `@import`, `@font-face`, positioned overlays).
 *     `<style>`/`<link>`/`<script>` tags are not in the allowlist either.
 *
 *  2. ISOLATE — the sanitised HTML is rendered inside a *sandboxed* iframe
 *     (via `srcdoc`), never in the main document. The sandbox withholds
 *     `allow-scripts` (no JS ever executes) and `allow-forms`, so email content
 *     cannot script the page or phish via forms. `allow-same-origin` is granted
 *     *without* `allow-scripts` — with no script able to run it grants the
 *     content zero active capability; it only lets the parent read the static
 *     DOM to auto-size the frame. `allow-popups`(+escape) lets a user click a
 *     link and open it as a normal tab.
 *
 *  3. STARVE — a strict `Content-Security-Policy` meta tag inside the frame
 *     sets `default-src 'none'`, blocking every remote fetch. Remote images are
 *     blocked by default (`img-src data:`) and only enabled behind an explicit
 *     "load remote content" opt-in (`img-src http: https: data:`), the standard
 *     webmail pattern that defeats tracking pixels. Scripts, objects, frames,
 *     fonts and network connections stay blocked in both modes.
 */

const ALLOWED_TAGS = [
  'p', 'br', 'b', 'i', 'u', 'strong', 'em', 's', 'sub', 'sup', 'small',
  'a', 'ul', 'ol', 'li', 'dl', 'dt', 'dd',
  'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
  'blockquote', 'pre', 'code', 'hr', 'img',
  'table', 'thead', 'tbody', 'tfoot', 'tr', 'th', 'td', 'caption',
  'span', 'div',
];

// Deliberately excludes `style` and `class` (CSS exfiltration vectors).
const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'width', 'height', 'target', 'rel'];

// Force every link to open in a fresh, isolated tab. Runs once at module load;
// this file is the only DOMPurify consumer in the app.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank');
    node.setAttribute('rel', 'noopener noreferrer nofollow');
  }
});

function sanitize(html: string): string {
  return DOMPurify.sanitize(html, {
    ALLOWED_TAGS,
    ALLOWED_ATTR,
    FORBID_TAGS: ['style', 'link', 'script', 'iframe', 'object', 'embed', 'form', 'base'],
    FORBID_ATTR: ['style', 'class', 'id'],
    ALLOW_DATA_ATTR: false,
  });
}

/** True if the sanitised HTML references any remote (http/https) resource. */
function hasRemoteResources(html: string): boolean {
  try {
    const doc = new DOMParser().parseFromString(html, 'text/html');
    return Array.from(doc.querySelectorAll('[src], [background]')).some((el) => {
      const ref = el.getAttribute('src') || el.getAttribute('background') || '';
      return /^https?:/i.test(ref.trim());
    });
  } catch {
    return false;
  }
}

interface Palette {
  scheme: 'light' | 'dark';
  bg: string;
  fg: string;
  link: string;
  border: string;
  muted: string;
}

function palette(dark: boolean): Palette {
  return dark
    ? { scheme: 'dark', bg: '#181a1f', fg: '#e6e6e6', link: '#7aa2f7', border: '#2c3038', muted: '#9aa0a6' }
    : { scheme: 'light', bg: '#ffffff', fg: '#1a1a1a', link: '#2563eb', border: '#e5e7eb', muted: '#666666' };
}

function buildSrcDoc(sanitized: string, allowRemote: boolean, dark: boolean): string {
  const p = palette(dark);
  const img = allowRemote ? 'http: https: data:' : 'data:';
  const csp = [
    "default-src 'none'",
    "style-src 'unsafe-inline'",
    `img-src ${img}`,
    `media-src ${img}`,
    "font-src data:",
    "script-src 'none'",
    "object-src 'none'",
    "frame-src 'none'",
    "child-src 'none'",
    "connect-src 'none'",
    "base-uri 'none'",
    "form-action 'none'",
  ].join('; ');

  return `<!doctype html><html><head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${csp}">
<meta name="referrer" content="no-referrer">
<style>
  :root { color-scheme: ${p.scheme}; }
  html, body { margin: 0; padding: 0; }
  body {
    background: ${p.bg};
    color: ${p.fg};
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: 13px;
    line-height: 1.6;
    padding: 2px 2px 8px;
    word-break: break-word;
    overflow-wrap: anywhere;
  }
  img { max-width: 100%; height: auto; }
  a { color: ${p.link}; }
  blockquote {
    border-left: 3px solid ${p.border};
    margin: 0 0 0 4px;
    padding-left: 12px;
    color: ${p.muted};
  }
  table { border-collapse: collapse; max-width: 100%; }
  th, td { border: 1px solid ${p.border}; padding: 4px 8px; }
  pre, code { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; white-space: pre-wrap; }
  hr { border: none; border-top: 1px solid ${p.border}; }
</style>
</head><body>${sanitized}</body></html>`;
}

const DARK_THEMES = ['midnight', 'forest', 'slate', 'dusk', 'neon', 'aurora', 'industrial'];

export function HtmlMessageBody({ html }: { html: string }) {
  // Subscribing to the theme re-renders (and recomputes the palette) on switch.
  const dark = useUIStore((s) => DARK_THEMES.includes(s.theme));

  const [allowRemote, setAllowRemote] = useState(false);
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const [height, setHeight] = useState(120);

  // Re-lock remote content whenever a different message is shown.
  useEffect(() => {
    setAllowRemote(false);
  }, [html]);

  const sanitized = useMemo(() => sanitize(html), [html]);
  const remotePresent = useMemo(() => hasRemoteResources(sanitized), [sanitized]);
  const srcDoc = useMemo(
    () => buildSrcDoc(sanitized, allowRemote, dark),
    [sanitized, allowRemote, dark],
  );

  // Auto-size the frame to its content. `allow-same-origin` (granted without
  // `allow-scripts`) lets us read the sanitised, script-free DOM for measuring.
  useLayoutEffect(() => {
    const iframe = iframeRef.current;
    if (!iframe) return;

    let observer: ResizeObserver | null = null;

    const measure = () => {
      const doc = iframe.contentDocument;
      if (!doc?.documentElement) return;
      const next = Math.max(doc.documentElement.scrollHeight, doc.body?.scrollHeight ?? 0);
      if (next > 0) setHeight(next);
    };

    const onLoad = () => {
      measure();
      const body = iframe.contentDocument?.body;
      if (body && 'ResizeObserver' in window) {
        observer = new ResizeObserver(measure);
        observer.observe(body);
      }
    };

    iframe.addEventListener('load', onLoad);
    // srcdoc may already be committed synchronously in some browsers.
    measure();

    return () => {
      iframe.removeEventListener('load', onLoad);
      observer?.disconnect();
    };
  }, [srcDoc]);

  return (
    <div>
      {remotePresent && !allowRemote && (
        <div className="flex items-center gap-2 mb-3 px-3 py-2 rounded-2xl bg-secondary text-muted-foreground font-mono text-xs">
          <ImageOff className="w-3.5 h-3.5 shrink-0" />
          <span className="flex-1">// remote content blocked to protect your privacy</span>
          <button
            onClick={() => setAllowRemote(true)}
            className="text-primary hover:underline font-medium shrink-0"
          >
            [load_remote_content]
          </button>
        </div>
      )}
      <iframe
        ref={iframeRef}
        title="Message content"
        srcDoc={srcDoc}
        sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"
        referrerPolicy="no-referrer"
        className="w-full block border-0 bg-transparent"
        style={{ height }}
      />
    </div>
  );
}
