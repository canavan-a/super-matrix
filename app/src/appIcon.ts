import {useEffect, useRef, useState} from 'react';
import {AppState, NativeModules, Platform} from 'react-native';

import {settingsStore} from './settings';
import type {ThemeName} from './theme';
import {useTheme} from './theme';

// The launcher icon follows the theme. Android has no API to swap an app's
// icon directly; instead the manifest declares one launcher entry per theme
// (an <activity-alias>, each with its own icon) and exactly one is enabled at
// a time — see android/.../AppIconModule.kt. Web and other platforms have
// nothing to switch, so everything here is a no-op there.

/** The theme every install starts on, i.e. the alias enabled in the manifest. */
export const DEFAULT_ICON: ThemeName = 'dark';

interface NativeAppIcon {
  setIcon(name: string): Promise<boolean>;
  getIcon(): Promise<string>;
}

function native(): NativeAppIcon | undefined {
  return Platform.OS === 'android' ? (NativeModules.AppIcon as NativeAppIcon | undefined) : undefined;
}

export async function setAppIcon(name: ThemeName): Promise<boolean> {
  const mod = native();
  if (!mod) return false;
  try {
    return await mod.setIcon(name);
  } catch {
    return false; // a launcher hiccup must never take the app down
  }
}

export async function getAppIcon(): Promise<ThemeName | null> {
  const mod = native();
  if (!mod) return null;
  try {
    return (await mod.getIcon()) as ThemeName;
  } catch {
    return null;
  }
}

/** The icon that should be showing, given the theme and the setting. */
export function wantedIcon(theme: ThemeName, enabled: boolean): ThemeName {
  return enabled ? theme : DEFAULT_ICON;
}

/**
 * Keeps the launcher icon in step with the theme, but only ever changes it as
 * the app goes to the background. Switching while the app is open can make some
 * launchers flicker, drop a pinned shortcut, or restart the app; done while the
 * user has already left, none of that is visible.
 */
export function useThemedAppIcon(): void {
  const theme = useTheme();
  const [enabled, setEnabled] = useState(settingsStore.get().themedIcon);
  useEffect(() => {
    settingsStore.load().then(s => setEnabled(s.themedIcon));
    return settingsStore.subscribe(s => setEnabled(s.themedIcon));
  }, []);

  const want = useRef<ThemeName>(wantedIcon(theme.name, enabled));
  want.current = wantedIcon(theme.name, enabled);
  // what the launcher is showing now (null until we've asked)
  const applied = useRef<ThemeName | null>(null);

  useEffect(() => {
    if (Platform.OS !== 'android') return;
    let alive = true;
    getAppIcon().then(cur => {
      if (alive) applied.current = cur;
    });
    const sub = AppState.addEventListener('change', state => {
      if (state !== 'background') return;
      const target = want.current;
      if (applied.current === target) return;
      setAppIcon(target).then(ok => {
        if (ok) applied.current = target;
      });
    });
    return () => {
      alive = false;
      sub.remove();
    };
  }, []);
}
