// Theming: a handful of named color palettes the user can switch between
// from Settings, rather than the app being permanently locked to the one
// light/blue look it shipped with. Layout (padding, flex, radii, gaps) stays
// in each screen's static StyleSheet — only color tokens come from here, so
// switching themes never has to touch geometry.
import React, {createContext, useContext, useEffect, useMemo, useState} from 'react';

import {settingsStore} from './settings';

export type ThemeName = 'light' | 'dark' | 'slate' | 'sepia' | 'ember';

export interface Theme {
  name: ThemeName;
  bg: string;
  surface: string;
  surfaceAlt: string;
  border: string;
  text: string;
  textMuted: string;
  primary: string;
  primaryText: string;
  danger: string;
  dangerBg: string;
  warning: string;
  warningBg: string;
  success: string;
  bubbleUser: string;
  bubbleUserText: string;
  bubbleAssistant: string;
  bubbleAssistantText: string;
  codeBg: string;
  codeBorder: string;
  codeText: string;
  inlineCodeBg: string;
}

const light: Theme = {
  name: 'light',
  bg: '#ffffff',
  surface: '#f6f8fa',
  surfaceAlt: '#f0f0f0',
  border: '#e5e5e5',
  text: '#1b1f24',
  textMuted: '#57606a',
  primary: '#0969da',
  primaryText: '#ffffff',
  danger: '#cf222e',
  dangerBg: '#ffebe9',
  warning: '#9a6700',
  warningBg: '#fff8c5',
  success: '#1a7f37',
  bubbleUser: '#0969da',
  bubbleUserText: '#ffffff',
  bubbleAssistant: '#f0f0f0',
  bubbleAssistantText: '#1b1f24',
  codeBg: '#f6f8fa',
  codeBorder: '#d0d7de',
  codeText: '#24292f',
  inlineCodeBg: '#e5e5e5',
};

const dark: Theme = {
  name: 'dark',
  bg: '#0d1117',
  surface: '#161b22',
  surfaceAlt: '#21262d',
  border: '#30363d',
  text: '#e6edf3',
  textMuted: '#8b949e',
  primary: '#2f81f7',
  primaryText: '#ffffff',
  danger: '#f85149',
  dangerBg: '#3d1418',
  warning: '#d29922',
  warningBg: '#3b2e0e',
  success: '#3fb950',
  bubbleUser: '#2f81f7',
  bubbleUserText: '#ffffff',
  bubbleAssistant: '#21262d',
  bubbleAssistantText: '#e6edf3',
  codeBg: '#161b22',
  codeBorder: '#30363d',
  codeText: '#c9d1d9',
  inlineCodeBg: '#2d333b',
};

const slate: Theme = {
  name: 'slate',
  bg: '#1e2530',
  surface: '#262e3b',
  surfaceAlt: '#2f3947',
  border: '#3c4757',
  text: '#dde3ea',
  textMuted: '#93a0b3',
  primary: '#5aa9e6',
  primaryText: '#0d1117',
  danger: '#ef6a6a',
  dangerBg: '#3a2222',
  warning: '#e3b341',
  warningBg: '#3a3220',
  success: '#6fcf97',
  bubbleUser: '#5aa9e6',
  bubbleUserText: '#0d1117',
  bubbleAssistant: '#2f3947',
  bubbleAssistantText: '#dde3ea',
  codeBg: '#232b37',
  codeBorder: '#3c4757',
  codeText: '#cdd6e0',
  inlineCodeBg: '#39424f',
};

const sepia: Theme = {
  name: 'sepia',
  bg: '#f4ecd8',
  surface: '#ece0c6',
  surfaceAlt: '#e5d7b8',
  border: '#d8c6a0',
  text: '#3b2f1e',
  textMuted: '#7a6a4d',
  primary: '#a0651b',
  primaryText: '#fdf6e7',
  danger: '#a13c2c',
  dangerBg: '#f0dcd2',
  warning: '#8a6510',
  warningBg: '#efe2b8',
  success: '#4e7c3a',
  bubbleUser: '#a0651b',
  bubbleUserText: '#fdf6e7',
  bubbleAssistant: '#e5d7b8',
  bubbleAssistantText: '#3b2f1e',
  codeBg: '#ece0c6',
  codeBorder: '#d8c6a0',
  codeText: '#3b2f1e',
  inlineCodeBg: '#dccca3',
};

// Same dark base as `dark`, swapping its blue accent for a warm red/orange
// one — buttons, the user's chat bubble, links, and syntax-highlight numbers
// all pick up the orange; danger stays a true red so "delete"/errors don't
// blend into the accent color.
const ember: Theme = {
  name: 'ember',
  bg: '#050403',
  surface: '#0c0908',
  surfaceAlt: '#150f0d',
  border: '#241815',
  text: '#e8d9d0',
  textMuted: '#8a6c5f',
  primary: '#8a3620',
  primaryText: '#f1e2d9',
  danger: '#c93a3a',
  dangerBg: '#1c0d0c',
  warning: '#c9952c',
  warningBg: '#1c160a',
  success: '#4f9e70',
  bubbleUser: '#8a3620',
  bubbleUserText: '#f1e2d9',
  bubbleAssistant: '#150f0d',
  bubbleAssistantText: '#e8d9d0',
  codeBg: '#0c0908',
  codeBorder: '#241815',
  codeText: '#d9c9bf',
  inlineCodeBg: '#1c130f',
};

export const THEMES: Record<ThemeName, Theme> = {light, dark, slate, sepia, ember};

export const THEME_OPTIONS: {name: ThemeName; label: string}[] = [
  {name: 'light', label: 'Light'},
  {name: 'dark', label: 'Dark'},
  {name: 'slate', label: 'Slate'},
  {name: 'sepia', label: 'Sepia'},
  {name: 'ember', label: 'Ember'},
];

const ThemeContext = createContext<{theme: Theme; setThemeName: (n: ThemeName) => void}>({
  theme: light,
  setThemeName: () => {},
});

export function ThemeProvider({children}: {children: React.ReactNode}): React.JSX.Element {
  const [themeName, setThemeNameState] = useState<ThemeName>('dark');

  useEffect(() => {
    settingsStore.load().then(s => setThemeNameState(s.themeName));
    return settingsStore.subscribe(s => setThemeNameState(s.themeName));
  }, []);

  const setThemeName = (n: ThemeName) => {
    settingsStore.save({...settingsStore.get(), themeName: n});
  };

  const value = useMemo(() => ({theme: THEMES[themeName], setThemeName}), [themeName]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): Theme {
  return useContext(ThemeContext).theme;
}

export function useThemeSetting(): {theme: Theme; themeName: ThemeName; setThemeName: (n: ThemeName) => void} {
  const {theme, setThemeName} = useContext(ThemeContext);
  return {theme, themeName: theme.name, setThemeName};
}
