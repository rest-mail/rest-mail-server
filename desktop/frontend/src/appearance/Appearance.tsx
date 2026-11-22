import { useEffect, useRef, useState, type CSSProperties } from 'react';
import { ArrowLeft, Check, ChevronRight, Mail, Paintbrush, Shuffle, SlidersHorizontal, Trash2 } from 'lucide-react';
import { appearance, appearanceSettings, resolveTheme, themeProperties, useAppearance, type RemixSpec } from './engine';
import { fontFamilies, fontStack, monoFamilies, type FontFamily } from './fonts';
import { builtInThemes, themeByName, type Theme } from './themes';
import './appearance.css';

const specimen = 'Good things in your inbox.';
const categories = [{ key: 'colours', label: 'Colours', detail: 'Surfaces, accents & atmosphere' }, { key: 'typefaces', label: 'Typography', detail: 'Display, interface, reading & code' }, { key: 'shape', label: 'Shape & rhythm', detail: 'Corners, spacing & type scale' }] as const;

export function ThemePreview({ theme }: { theme: Theme }) {
  return <div className="appearance-preview" style={themeProperties(theme) as CSSProperties} aria-label="Live theme preview" data-testid="appearance-preview">
    <div className="appearance-preview-sidebar"><strong>REST Mail</strong><span className="appearance-preview-compose">＋ New message</span><span className="appearance-preview-inbox">Inbox <b>3</b></span><span>Sent</span><span>Drafts</span><small>Made for your day.</small></div>
    <div className="appearance-preview-list"><small>TODAY</small><div><b>Ada Lovelace</b><span>A little inspiration</span><small>Something worth opening.</small></div><div><b>The Sunday Edit</b><span>Room to think.</span><small>A fresh perspective.</small></div></div>
    <div className="appearance-preview-reader"><small>FROM ADA, WITH LOVE</small><h3>A little<br />room to think.</h3><p>Less noise. More of what matters.</p><div className="appearance-preview-signin"><span aria-hidden="true">{'{'}</span><span>Welcome home.<i /></span><span aria-hidden="true">{'}'}</span></div></div>
  </div>;
}

function ThemeCard({ theme, selected, onSelect }: { theme: Theme; selected: boolean; onSelect: () => void }) {
  return <button className="appearance-theme-card" data-testid={`theme-${theme.id}`} aria-pressed={selected} onClick={onSelect} style={{ '--specimen-font': fontStack(theme.fonts.display), '--specimen-ui': fontStack(theme.fonts.ui), '--specimen-bg': theme.colours.list, '--specimen-ink': theme.colours.ink, '--specimen-sidebar': theme.colours.sidebar, '--specimen-accent': theme.colours.accent } as CSSProperties}>
    <div className="appearance-theme-specimen"><span>{theme.name}</span><small style={{ fontFamily: fontStack(theme.fonts.reading) }}>{specimen}</small><div className="appearance-swatches" aria-hidden="true">{[theme.colours.sidebar, theme.colours.selection, theme.colours.accent, theme.colours.reader].map((colour, index) => <i key={index} style={{ background: colour }} />)}</div>{selected && <b className="appearance-selected"><Check size={12} /></b>}</div>
    <span className="appearance-theme-name">{theme.name}<small>{theme.dark ? 'Dark' : 'Light'}</small></span><span className="appearance-theme-character">{theme.character}</span>
  </button>;
}

export function AppearanceSettings() {
  const state = useAppearance();
  const theme = resolveTheme(state.spec);
  const [section, setSection] = useState<'collection' | 'remix' | 'saved'>('collection');
  const [sourceA, setSourceA] = useState(state.spec.colours);
  const [sourceB, setSourceB] = useState(state.spec.typefaces === state.spec.colours ? 'midnight' : state.spec.typefaces);
  const [name, setName] = useState('');
  const [notice, setNotice] = useState('');
  const dialog = useRef<HTMLDivElement>(null);
  const done = useRef<HTMLButtonElement>(null);
  const selected = state.saved.find(item => item.id === state.selected)?.name ?? builtInThemes.find(item => item.id === state.selected)?.name ?? 'Custom';
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    done.current?.focus();
    const keyboard = (event: KeyboardEvent) => {
      if (event.key === 'Escape') { event.preventDefault(); appearanceSettings.close(); }
      if (event.key !== 'Tab') return;
      const controls = [...dialog.current!.querySelectorAll<HTMLElement>('button, input, select, [tabindex="0"]')].filter(element => !element.matches(':disabled'));
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
      if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
    };
    document.addEventListener('keydown', keyboard);
    return () => { document.removeEventListener('keydown', keyboard); previous?.focus(); };
  }, []);
  const changeSource = (side: 'a' | 'b', id: string) => {
    const previous = side === 'a' ? sourceA : sourceB;
    if (side === 'a') setSourceA(id); else setSourceB(id);
    const patch: Partial<RemixSpec> = {};
    for (const category of categories) if (state.spec[category.key] === previous) patch[category.key] = id;
    appearance.remix(patch);
  };
  const choose = (id: string) => { appearance.select(id); setSourceA(id); setSourceB(id === 'midnight' ? 'letterpress' : 'midnight'); setNotice(''); };
  const surprise = () => { appearance.surprise(); const spec = appearance.get().spec; setSourceA(spec.colours); setSourceB(spec.typefaces); setNotice('A fresh combination. Keep what you love.'); };
  const save = () => { try { const saved = appearance.save(name); setName(''); setNotice(`Saved “${saved.name}”.`); } catch (error) { setNotice(error instanceof Error ? error.message : 'Could not save theme.'); } };
  return <div className="appearance-settings" role="dialog" aria-modal="true" aria-labelledby="appearance-title" ref={dialog} data-testid="appearance-settings">
    <header className="appearance-windowbar"><span>Settings</span><button ref={done} data-testid="appearance-done" onClick={appearanceSettings.close}><ArrowLeft size={14} /> Back to app <kbd>esc</kbd></button></header>
    <div className="appearance-workspace">
      <nav className="appearance-nav" aria-label="Settings sections"><span className="appearance-nav-label">YOUR WORKSPACE</span><button className="appearance-nav-current" aria-current="page"><Paintbrush size={16} /> Appearance <ChevronRight size={14} /></button><div className="appearance-nav-note"><span aria-hidden="true">Aa.</span><p>A mail client.<br />A thousand personalities.</p><small>Only on this device.<br />Made entirely yours.</small></div></nav>
      <main className="appearance-main">
        <div className="appearance-hero"><div><span className="appearance-eyebrow">A LITTLE MORE YOU</span><h1 id="appearance-title">Make yourself<br />at home.</h1><p>Ten starting points. Twenty typefaces.<br />Endless ways to make mail feel like yours.</p><span className="appearance-current"><i /> Live now <b>{selected}</b></span></div><div className="appearance-preview-wrap"><div className="appearance-preview-label"><span><Mail size={12} /> YOUR INBOX, REIMAGINED</span><span>Live preview</span></div><ThemePreview theme={theme} /></div></div>
        <div className="appearance-sectionbar"><div role="tablist" aria-label="Appearance sections">{([['collection', 'The collection'], ['remix', 'Remix studio'], ['saved', `Your themes${state.saved.length ? ` · ${state.saved.length}` : ''}`]] as const).map(([id, label]) => <button role="tab" key={id} id={`appearance-tab-${id}`} aria-controls={`appearance-panel-${id}`} data-testid={`appearance-${id}`} aria-selected={section === id} onClick={() => { setSection(id); setNotice(''); }}>{label}</button>)}</div><button className="appearance-surprise" data-testid="appearance-surprise" onClick={surprise}><Shuffle size={14} /> Surprise me</button></div>
        {section === 'collection' && <section role="tabpanel" id="appearance-panel-collection" aria-labelledby="appearance-tab-collection"><div className="appearance-collection">{builtInThemes.map(item => <ThemeCard key={item.id} theme={item} selected={state.selected === item.id} onSelect={() => choose(item.id)} />)}</div><div className="appearance-footnote"><span>Every palette is contrast-tested. Every font is bundled offline.</span><button onClick={() => setSection('remix')}>Find your own combination <SlidersHorizontal size={13} /></button></div></section>}
        {section === 'remix' && <section role="tabpanel" id="appearance-panel-remix" aria-labelledby="appearance-tab-remix" className="appearance-remix">
          <div className="appearance-remix-main"><h2>Better together.</h2><p>Two themes. Take your favourite parts of each.</p><div className="appearance-sources">{([['a', 'Theme A', sourceA], ['b', 'Theme B', sourceB]] as const).map(([side, label, source]) => <label key={side}><span>{label}</span><select aria-label={label} data-testid={`remix-source-${side}`} value={source} onChange={event => changeSource(side, event.target.value)}>{builtInThemes.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>)}</div>
            <div className="appearance-categories">{categories.map(category => <div key={category.key}><span><b>{category.label}</b><small>{category.detail}</small></span><div role="group" aria-label={`${category.label} source`}>{[sourceA, sourceB].map((source, index) => <button key={`${index}-${source}`} data-testid={`remix-${category.key}-${index === 0 ? 'a' : 'b'}`} aria-pressed={state.spec[category.key] === source} onClick={() => appearance.remix({ [category.key]: source })}>{index === 0 ? 'A' : 'B'}<span>{themeByName(source).name}</span></button>)}</div></div>)}</div>
            <div className="appearance-save"><label htmlFor="appearance-save-name">Keep this combination</label><div><input id="appearance-save-name" data-testid="theme-save-name" placeholder="Give your theme a name…" maxLength={60} value={name} onChange={event => setName(event.target.value)} onKeyDown={event => { if (event.key === 'Enter') save(); }} /><button data-testid="theme-save" onClick={save}>Save theme</button></div><small>Your current edits are saved automatically as Custom.</small></div>
          </div>
          <div className="appearance-overrides"><div className="appearance-overrides-title"><h2>The finishing touches</h2><button data-testid="theme-reset" onClick={() => { appearance.reset(); setSourceA(state.spec.colours); setNotice('Back to the original theme.'); }}>Reset to theme</button></div><label className="appearance-accent"><span>Accent colour<small>Button text adapts for contrast.</small></span><input type="color" aria-label="Accent colour" data-testid="theme-accent" value={theme.colours.accent} onChange={event => appearance.override({ accent: event.target.value })} /><code>{theme.colours.accent.toUpperCase()}</code></label>
            <div className="appearance-fonts">{(['display', 'ui', 'reading', 'mono'] as const).map(role => <label key={role}><span>{({ display: 'Display', ui: 'Interface', reading: 'Reading', mono: 'Monospace' })[role]}</span><select aria-label={`${role} font`} data-testid={`theme-font-${role}`} value={theme.fonts[role]} style={{ fontFamily: fontStack(theme.fonts[role]) }} onChange={event => appearance.override({ [role]: event.target.value as FontFamily })}>{(role === 'mono' ? monoFamilies : fontFamilies).map(family => <option key={family} value={family}>{family}</option>)}</select></label>)}</div>
            <label className="appearance-slider"><span>Corner radius <output>{theme.shape.radius}px</output></span><input type="range" aria-label="Corner radius" data-testid="theme-radius" min="0" max="24" value={theme.shape.radius} onChange={event => appearance.override({ radius: Number(event.target.value) })} /><small>Sharp <span>Soft</span></small></label><label className="appearance-slider"><span>Row spacing <output>{theme.shape.density}px</output></span><input type="range" aria-label="Row spacing" data-testid="theme-density" min="28" max="48" value={theme.shape.density} onChange={event => appearance.override({ density: Number(event.target.value) })} /><small>Compact <span>Roomy</span></small></label>
          </div>
        </section>}
        {section === 'saved' && <section role="tabpanel" id="appearance-panel-saved" aria-labelledby="appearance-tab-saved" className="appearance-saved">{state.saved.length ? state.saved.map(item => <div key={item.id}><ThemeCard theme={{ ...resolveTheme(item.spec), id: item.id, name: item.name, character: `${themeByName(item.spec.colours).name} colours · ${themeByName(item.spec.typefaces).name} type` }} selected={state.selected === item.id} onSelect={() => { appearance.select(item.id); setSourceA(item.spec.colours); setSourceB(item.spec.typefaces); }} /><button aria-label={`Delete ${item.name}`} data-testid={`theme-delete-${item.id}`} onClick={() => { appearance.remove(item.id); setNotice(`Deleted “${item.name}”.`); }}><Trash2 size={13} /> Delete theme</button></div>) : <div className="appearance-empty"><Paintbrush size={26} /><h2>Your next favourite is yours to make.</h2><p>Mix a palette with a new set of typefaces, then give it a name.</p><button onClick={() => setSection('remix')}>Open the remix studio <ChevronRight size={14} /></button></div>}</section>}
        <p role="status" className="appearance-notice" data-testid="appearance-notice">{notice}</p>
      </main>
    </div>
  </div>;
}
