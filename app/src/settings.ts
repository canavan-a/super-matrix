import AsyncStorage from '@react-native-async-storage/async-storage';

// Super Matrix has no shared default server: everyone runs their own. The
// app keeps a list of servers it has signed in to and which one is open.
const STORAGE_KEY = 'supermatrix.settings.v1';

export interface Settings {
  // Every server this device has signed in to (admin URLs).
  servers: string[];
  // The one the app is showing; null on the sign-in screen.
  activeServer: string | null;
  // See src/theme.tsx for the palettes.
  themeName: 'light' | 'dark' | 'slate' | 'sepia' | 'ember';
  // Android-only: switch the launcher icon to the one matching the current
  // theme (see src/appIcon.ts).
  themedIcon: boolean;
  // Play the animated launch splash.
  launchSplash: boolean;
  // Crash-loop protection for the splash: a launch counts as a strike before
  // the animation starts and is cleared when it finishes. After
  // MAX_SPLASH_STRIKES in a row the splash turns itself off.
  splashStrikes: number;
  splashAutoDisabled: boolean;
}

const DEFAULT_SETTINGS: Settings = {
  servers: [],
  activeServer: null,
  themeName: 'dark',
  themedIcon: true,
  launchSplash: true,
  splashStrikes: 0,
  splashAutoDisabled: false,
};

type Listener = (settings: Settings) => void;

let current: Settings = DEFAULT_SETTINGS;
let loaded = false;
const listeners = new Set<Listener>();

async function load(): Promise<Settings> {
  if (loaded) {
    return current;
  }
  try {
    const raw = await AsyncStorage.getItem(STORAGE_KEY);
    if (raw) {
      current = {...DEFAULT_SETTINGS, ...JSON.parse(raw)};
    }
  } catch {
    // fall back to defaults — a corrupt/missing value shouldn't crash startup
  }
  loaded = true;
  return current;
}

async function save(next: Settings): Promise<void> {
  current = next;
  listeners.forEach(l => l(current));
  try {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // best-effort persistence; the in-memory value above is still updated
  }
}

async function update(patch: Partial<Settings>): Promise<void> {
  await save({...current, ...patch});
}

export const settingsStore = {
  get: (): Settings => current,
  load,
  save,
  update,
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
};

export function normalizeServer(url: string): string {
  let u = url.trim().replace(/\/+$/, '');
  if (u && !/^https?:\/\//.test(u)) u = `https://${u}`;
  return u;
}
