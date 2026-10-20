// New-version notification and the notification-permission nag. Android
// only, and like channels.ts only ever imported dynamically from shared code.
//
// The update notification is deliberately hard to get rid of: ongoing (no
// swipe-away on most Android versions), on its own high-importance channel,
// and re-posted on every check while the installed build is behind. It
// clears itself once the installed version catches up.
import notifee, {AndroidImportance} from '@notifee/react-native';
import {Alert, Linking} from 'react-native';

import {compareVersions, fetchReleases, getVersionInfo} from '../appUpdater';
import {ensureNotificationPermission, notificationColor, PREFER_SMALL_ICON, PRESS_ACTION} from './channels';
import {SETTINGS_NAV} from './pendingNav';

const CH_UPDATES = 'supermatrix-updates';
const NID_UPDATE = 'supermatrix-update';

/** Don't hit GitHub more often than this from foreground checks. */
const MIN_INTERVAL_MS = 30 * 60_000;
let lastCheck = 0;

/**
 * Looks for a newer release and shows (or clears) the update notification.
 * Never throws: an offline check just tries again next time.
 */
export async function checkForUpdate({force = false} = {}): Promise<void> {
  if (!force && Date.now() - lastCheck < MIN_INTERVAL_MS) return;
  lastCheck = Date.now();
  try {
    const [{versionName}, releases] = await Promise.all([getVersionInfo(), fetchReleases()]);
    const latest = releases[0];
    if (!latest || compareVersions(latest.version, versionName) <= 0) {
      await notifee.cancelNotification(NID_UPDATE);
      return;
    }
    await notifee.createChannel({id: CH_UPDATES, name: 'App updates', importance: AndroidImportance.HIGH});
    await notifee.displayNotification({
      id: NID_UPDATE,
      title: `Super Matrix v${latest.version} is out`,
      body: `You have v${versionName}. Tap to update.`,
      data: {nav: SETTINGS_NAV, ...PREFER_SMALL_ICON},
      android: {
        channelId: CH_UPDATES,
        smallIcon: 'ic_notification',
        color: await notificationColor(),
        pressAction: PRESS_ACTION,
        ongoing: true,
        autoCancel: false,
        // Heads-up once per version, then it just stays.
        onlyAlertOnce: true,
      },
    });
  } catch {
    // offline, rate-limited, or no native module: try again next time
  }
}

/**
 * Asks for notification permission if it isn't granted. Android stops showing
 * its own dialog after two refusals, so then this explains and offers the
 * system settings instead.
 */
export async function nagForPermission(): Promise<void> {
  try {
    if (await ensureNotificationPermission()) return;
    Alert.alert(
      'Notifications are off',
      "Super Matrix can't tell you about app updates. Turn notifications on in Settings → Apps → Super Matrix → Notifications.",
      [
        {text: 'Not now', style: 'cancel'},
        {text: 'Open settings', onPress: () => Linking.openSettings()},
      ],
    );
  } catch {
    // no notifee (shouldn't happen on Android); nothing to nag about
  }
}
