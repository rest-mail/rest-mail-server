import { useEffect } from 'react';
import { resolveTheme, useAppearance } from './engine';
import { embeddedFontCSS, fontStack } from './fonts';

export function framePaperStyle(readingFont: string) {
  return `:root { color-scheme: light; } body { background: #ffffff; color: #1a2a4a; font-family: ${readingFont}; padding: 18px 20px; } a { color: #2c4373; } blockquote { color: #53627a; border-color: #7b879b; } th, td, hr { border-color: #7b879b; }`;
}

export function useReaderPaper() {
  const state = useAppearance();
  const family = resolveTheme(state.spec).fonts.reading;
  useEffect(() => {
    let active = true;
    const paint = async (frame: HTMLIFrameElement) => {
      try {
        const fonts = await embeddedFontCSS(family).catch(() => '');
        if (!active || !frame.isConnected) return;
        const current = frame.getAttribute('srcdoc');
        if (!current || !current.includes('</head>')) return;
        const original = current.replace(/<style data-desktop-paper>[\s\S]*?<\/style>/g, '');
        const patched = original.replace('</head>', `<style data-desktop-paper>${fonts}\n${framePaperStyle(fontStack(family))}</style></head>`);
        if (current !== patched) frame.setAttribute('srcdoc', patched);
      } catch {}
    };
    const scan = () => {
      for (const frame of document.querySelectorAll<HTMLIFrameElement>('.desktop-reader-content iframe[title="Message content"]')) {
        void paint(frame);
      }
    };
    const observer = new MutationObserver(scan);
    observer.observe(document.body, { childList: true, subtree: true, attributes: true, attributeFilter: ['srcdoc'] });
    scan();
    return () => { active = false; observer.disconnect(); };
  }, [family]);
}
