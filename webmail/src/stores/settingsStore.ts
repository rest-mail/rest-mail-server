import { create } from 'zustand';

export type ReadingPane = 'bottom' | 'right' | 'off';
export type Density = 'comfortable' | 'compact';

interface Settings {
  readingPane: ReadingPane;
  density: Density;
  autoSaveDrafts: boolean;
  desktopNotifications: boolean;
  newMailSound: boolean;
}

interface SettingsState extends Settings {
  update: (patch: Partial<Settings>) => void;
}

function load(): Settings {
  try {
    const raw = localStorage.getItem('restmail-settings');
    if (raw) return { ...defaults(), ...JSON.parse(raw) };
  } catch { /* ignore */ }
  return defaults();
}

function defaults(): Settings {
  return {
    readingPane: 'bottom',
    density: 'comfortable',
    autoSaveDrafts: true,
    desktopNotifications: false,
    newMailSound: false,
  };
}

export const useSettingsStore = create<SettingsState>((set) => ({
  ...load(),

  update: (patch) => set(state => {
    const next = { ...state, ...patch };
    localStorage.setItem('restmail-settings', JSON.stringify({
      readingPane: next.readingPane,
      density: next.density,
      autoSaveDrafts: next.autoSaveDrafts,
      desktopNotifications: next.desktopNotifications,
      newMailSound: next.newMailSound,
    }));
    return next;
  }),
}));
