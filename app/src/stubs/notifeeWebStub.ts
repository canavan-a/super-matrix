// Stand-in for @notifee/react-native on the web build. Real notifee is a
// native-only module (its CJS internals `require()` a bare RN path that
// doesn't exist under react-native-web, which crashes Vite's dev-server
// dependency prescan the moment anything on the page even dynamically
// imports it — confirmed live: `run-app-web` failed outright with
// "Could not read from file: .../react-native-web/Libraries/vendor/emitter/
// EventEmitter" the moment BackgroundMonitorService.ts became reachable via
// import(), even though that import only ever executes behind a
// Platform.OS === 'android' guard that's never true in a browser).
//
// Every call site that touches notifee is Android-only and dynamically
// imported (see App.tsx, SettingsScreen.tsx, BackgroundMonitorService.ts),
// so on web this module is aliased in (see vite.config.ts) purely so Vite
// never has to resolve or parse the real package at all — its exports here
// are never actually invoked.
const noop = async () => undefined;

const notifee = {
  createChannel: noop,
  displayNotification: noop,
  getDisplayedNotifications: async () => [],
  cancelNotification: noop,
  stopForegroundService: noop,
  registerForegroundService: () => {},
  requestPermission: noop,
  getNotificationSettings: async () => ({authorizationStatus: 1}),
  isChannelBlocked: async () => false,
  openNotificationSettings: noop,
  getInitialNotification: async () => null,
  onForegroundEvent: () => () => {},
  onBackgroundEvent: () => {},
};

export default notifee;

export enum AndroidImportance {
  NONE = 0,
  MIN = 1,
  LOW = 2,
  DEFAULT = 3,
  HIGH = 4,
}

export enum AndroidForegroundServiceType {
  FOREGROUND_SERVICE_TYPE_DATA_SYNC = 1,
}

export enum EventType {
  UNKNOWN = -1,
  DISMISSED = 0,
  PRESS = 1,
  ACTION_PRESS = 2,
  DELIVERED = 3,
  APP_BLOCKED = 4,
  CHANNEL_BLOCKED = 5,
  CHANNEL_GROUP_BLOCKED = 6,
  TRIGGER_NOTIFICATION_CREATED = 7,
  FG_ALREADY_EXIST = 8,
}

export enum AndroidVisibility {
  SECRET = -1,
  PRIVATE = 0,
  PUBLIC = 1,
}

export enum AuthorizationStatus {
  NOT_DETERMINED = -1,
  DENIED = 0,
  AUTHORIZED = 1,
  PROVISIONAL = 2,
}

export enum AndroidStyle {
  BIGPICTURE = 0,
  BIGTEXT = 1,
  INBOX = 2,
  MESSAGING = 3,
}
