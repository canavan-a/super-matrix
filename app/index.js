/**
 * @format
 */

import notifee, {EventType} from '@notifee/react-native';
import {AppRegistry} from 'react-native';
import App from './src/App';
import {name as appName} from './app.json';
import {navTarget, pendingNav} from './src/notifications/pendingNav';

// A notification tap while the app is alive but backgrounded: there's no
// mounted React tree here, so the target is stashed in pendingNav for App.tsx.
// (Killed app: notifee.getInitialNotification() in App.tsx. Foreground:
// notifee.onForegroundEvent, also in App.tsx.)
notifee.onBackgroundEvent(async ({type, detail}) => {
  if (type === EventType.PRESS) {
    const target = navTarget(detail.notification?.data);
    if (target) pendingNav.set(target);
    // The update notification stays until the update is installed.
    if (detail.notification?.id && !detail.notification.android?.ongoing) {
      await notifee.cancelNotification(detail.notification.id);
    }
  }
});

AppRegistry.registerComponent(appName, () => App);
