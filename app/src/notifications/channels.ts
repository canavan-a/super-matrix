// Notifee setup, as in super-badger and super-chat. Only ever imported
// dynamically from Android-only code paths, never statically from a file the
// Vite web build bundles.
import notifee, {AuthorizationStatus} from '@notifee/react-native';

import {rgbToHex, themeAccent} from '../logo/logoTheme';
import {settingsStore} from '../settings';
import {THEMES} from '../theme';

// Tap target for every notification. Not Notifee's 'default' launch activity:
// that resolves to whichever launcher alias is enabled (.IconLight, .IconDark,
// … — see AndroidManifest.xml), which isn't a class Notifee can start. Name
// the real activity instead.
export const PRESS_ACTION = {id: 'default', launchActivity: 'com.supermatrixapp.MainActivity'};

// Spread into every notification's `data`: Notification.EXTRA_PREFER_SMALL_ICON,
// so Android 17+ shows the broom icon tinted with the notification's color
// rather than the light theme's launcher icon. patches/ lets Notifee pass a
// boolean through.
export const PREFER_SMALL_ICON = {'android.app.preferSmallIcon': true as unknown as string};

/** The current theme's accent, for tinting notifications. */
export async function notificationColor(): Promise<string> {
  const {themeName} = await settingsStore.load();
  return rgbToHex(themeAccent(THEMES[themeName] ?? THEMES.dark));
}

// Asking Android for notification permission. It handles one request at a
// time and silently drops any other made meanwhile, which never answers. So:
// check first (instant, no dialog), ask only when it isn't granted, share one
// request between callers, and never wait on it for ever.
let asking: Promise<boolean> | null = null;

/** True when notifications are allowed (asking, with Android's dialog, only if they aren't yet). */
export async function ensureNotificationPermission(): Promise<boolean> {
  const allowed = async () =>
    (await notifee.getNotificationSettings()).authorizationStatus === AuthorizationStatus.AUTHORIZED;
  if (await allowed()) return true;
  asking ??= (async () => {
    try {
      const res = await Promise.race([
        notifee.requestPermission(),
        new Promise<null>(resolve => setTimeout(() => resolve(null), 20_000)),
      ]);
      return res ? res.authorizationStatus === AuthorizationStatus.AUTHORIZED : await allowed();
    } finally {
      asking = null;
    }
  })();
  return asking;
}
