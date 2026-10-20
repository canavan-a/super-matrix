/**
 * @format
 */
import 'react-native';
import React from 'react';
import {AppState, NativeModules, Platform} from 'react-native';
import {afterEach, beforeEach, describe, expect, it, jest} from '@jest/globals';
import renderer, {act} from 'react-test-renderer';

import {getAppIcon, setAppIcon, useThemedAppIcon} from '../src/appIcon';
import {settingsStore} from '../src/settings';
import {ThemeProvider} from '../src/theme';

function Probe() {
  useThemedAppIcon();
  return null;
}

describe('themed launcher icon', () => {
  // every AppState listener registered (React Native and other libraries add
  // their own, so we can't assume ours is the only one)
  let listeners: ((state: string) => void)[] = [];
  let setIcon: jest.Mock<(name: string) => Promise<boolean>>;
  let getIcon: jest.Mock<() => Promise<string>>;
  let current: string;

  const flush = () => act(async () => {});
  const background = async () => {
    await act(async () => listeners.forEach(l => l('background')));
    await flush();
  };

  beforeEach(async () => {
    jest.replaceProperty(Platform, 'OS', 'android');
    current = 'light';
    setIcon = jest.fn(async (name: string) => {
      current = name;
      return true;
    });
    getIcon = jest.fn(async () => current);
    (NativeModules as any).AppIcon = {setIcon, getIcon};
    jest.spyOn(AppState, 'addEventListener').mockImplementation(((_t: string, cb: (s: string) => void) => {
      listeners.push(cb);
      return {remove: () => {}};
    }) as any);
    await settingsStore.load();
    await settingsStore.save({...settingsStore.get(), themeName: 'light', themedIcon: true});
  });

  afterEach(() => {
    jest.restoreAllMocks();
    delete (NativeModules as any).AppIcon;
    listeners = [];
  });

  const mount = async () => {
    let tree!: renderer.ReactTestRenderer;
    await act(async () => {
      tree = renderer.create(
        <ThemeProvider>
          <Probe />
        </ThemeProvider>,
      );
    });
    await flush();
    return tree;
  };

  it('does nothing while the app is open, even after the theme changes', async () => {
    const tree = await mount();
    await act(async () => settingsStore.save({...settingsStore.get(), themeName: 'ember'}));
    await flush();
    expect(setIcon).not.toHaveBeenCalled();
    await act(async () => tree.unmount());
  });

  it('switches to the current theme\'s icon as the app goes to the background', async () => {
    const tree = await mount();
    await act(async () => settingsStore.save({...settingsStore.get(), themeName: 'ember'}));
    await flush();
    await background();
    expect(setIcon).toHaveBeenCalledTimes(1);
    expect(setIcon).toHaveBeenCalledWith('ember');
    await act(async () => tree.unmount());
  });

  it('ignores other app states and does not re-apply an icon that is already showing', async () => {
    const tree = await mount();
    await act(async () => settingsStore.save({...settingsStore.get(), themeName: 'slate'}));
    await flush();
    await act(async () => listeners.forEach(l => l('active')));
    await act(async () => listeners.forEach(l => l('inactive')));
    expect(setIcon).not.toHaveBeenCalled();
    await background();
    await background();
    await background();
    expect(setIcon).toHaveBeenCalledTimes(1);
    await act(async () => tree.unmount());
  });

  it('makes no call at all if the icon already matches the theme', async () => {
    const tree = await mount(); // theme light, icon light
    await background();
    expect(setIcon).not.toHaveBeenCalled();
    await act(async () => tree.unmount());
  });

  it('goes back to the default icon when the setting is switched off', async () => {
    await act(async () => settingsStore.save({...settingsStore.get(), themeName: 'ember'}));
    current = 'ember';
    const tree = await mount();
    await act(async () => settingsStore.save({...settingsStore.get(), themedIcon: false}));
    await flush();
    await background();
    expect(setIcon).toHaveBeenCalledWith('dark');
    await act(async () => tree.unmount());
  });

  it('survives the native side failing', async () => {
    setIcon.mockRejectedValue(new Error('launcher said no'));
    const tree = await mount();
    await act(async () => settingsStore.save({...settingsStore.get(), themeName: 'sepia'}));
    await flush();
    await expect(background()).resolves.toBeUndefined();
    expect(await setAppIcon('dark')).toBe(false);
    await act(async () => tree.unmount());
  });

  it('is a harmless no-op off Android', async () => {
    jest.replaceProperty(Platform, 'OS', 'web');
    expect(await setAppIcon('ember')).toBe(false);
    expect(await getAppIcon()).toBeNull();
    await act(async () => {
      await settingsStore.save({...settingsStore.get(), themeName: 'ember'});
    });
    const tree = await mount();
    await background(); // fire every listener, ours included
    expect(setIcon).not.toHaveBeenCalled();
    await act(async () => tree.unmount());
  });
});
