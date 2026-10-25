import React, {useCallback, useEffect, useState} from 'react';
import {
  ActivityIndicator,
  AppState,
  BackHandler,
  Dimensions,
  Keyboard,
  Platform,
  Pressable,
  StatusBar,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import {SafeAreaProvider, useSafeAreaInsets} from 'react-native-safe-area-context';

import {useThemedAppIcon} from './appIcon';
import {CrashScreen} from './components/CrashScreen';
import {Icon} from './components/Icon';
import {PixelField} from './components/PixelField';
import {PortalOutlet, PortalProvider} from './components/Portal';
import {isSideload} from './distribution';
import {LogoLine} from './logo/LogoArt';
import {SplashGate} from './logo/SplashGate';
import {splashHold} from './logo/splashHold';
import {navTarget, pendingNav, SETTINGS_NAV} from './notifications/pendingNav';
import {setThemeColor, startPwa} from './pwa/pwa';
import {Route} from './routes';
import {serverLabel} from './settings';
import {DeploymentScreen} from './screens/DeploymentScreen';
import {HomeScreen} from './screens/HomeScreen';
import {LoginScreen} from './screens/LoginScreen';
import {DomainsScreen} from './screens/DomainsScreen';
import {RelaysScreen} from './screens/RelaysScreen';
import {SettingsScreen} from './screens/SettingsScreen';
import {init, useStore, wake} from './store';
import {Theme, ThemeProvider, useTheme} from './theme';

/**
 * Decides what's on screen: the sign-in screen (no session for the open
 * server) or the app itself.
 */
function SessionGate(): React.JSX.Element {
  const theme = useTheme();
  const phase = useStore(s => s.phase);

  // The launch splash doubles as the loading screen: hold it until there's
  // something to show (see splashHold). The sign-in screen shows at once;
  // the deployment list lets go once it has loaded.
  useEffect(() => {
    splashHold.hold();
    init().catch(() => {});
  }, []);
  useEffect(() => {
    if (phase === 'signedOut') splashHold.release();
  }, [phase]);

  if (phase === 'loading') {
    return (
      <View style={[styles.fill, {backgroundColor: theme.bg}]}>
        <ActivityIndicator color={theme.textMuted} />
      </View>
    );
  }
  if (phase === 'signedOut') return <LoginScreen />;
  return <AppInner />;
}

const TITLES: Record<Route['name'], string> = {
  home: 'Super Matrix',
  deployment: 'Deployment',
  relays: 'Relays',
  domains: 'Domains',
  settings: 'Settings',
};

function AppInner(): React.JSX.Element {
  const theme = useTheme();
  useThemedAppIcon();
  const styles = makeStyles(theme);
  const loaded = useStore(s => s.loaded);
  const error = useStore(s => s.error);
  const server = useStore(s => s.server);
  const deployments = useStore(s => s.deployments);
  useEffect(() => {
    if (loaded) splashHold.release();
  }, [loaded]);

  const [route, setRoute] = useState<Route>({name: 'home'});
  const navigate = useCallback((next: Route) => setRoute(next), []);
  const goBack = useCallback(() => setRoute({name: 'home'}), []);

  // Back in the foreground: refetch, and reconnect the live socket at once.
  useEffect(() => {
    const sub = AppState.addEventListener('change', state => {
      if (state === 'active') wake();
    });
    return () => sub.remove();
  }, []);

  // Every launch: ask for notifications if they're off, then look for a new
  // version (which posts the sticky update notification). The update check
  // repeats on each return to the foreground; it throttles itself.
  useEffect(() => {
    if (Platform.OS !== 'android' || !isSideload) return;
    const updates = import('./notifications/updates');
    updates.then(async ({nagForPermission, checkForUpdate}) => {
      await nagForPermission();
      await checkForUpdate({force: true});
    });
    const sub = AppState.addEventListener('change', state => {
      if (state === 'active') updates.then(({checkForUpdate}) => checkForUpdate());
    });
    return () => sub.remove();
  }, []);

  // The update notification's tap opens Settings, from any app state (see
  // index.js).
  useEffect(() => {
    if (Platform.OS !== 'android') return;
    let unsubscribeForeground: (() => void) | undefined;
    const open = (target: string | null) => {
      if (target === SETTINGS_NAV) navigate({name: 'settings'});
    };
    import('@notifee/react-native').then(({default: notifee, EventType}) => {
      notifee.getInitialNotification().then(initial => open(navTarget(initial?.notification.data)));
      unsubscribeForeground = notifee.onForegroundEvent(({type, detail}) => {
        if (type === EventType.PRESS) open(navTarget(detail.notification?.data));
      });
    });
    const unsubscribePending = pendingNav.subscribe(key => {
      pendingNav.clear();
      open(key);
    });
    const already = pendingNav.get();
    if (already !== null) {
      pendingNav.clear();
      open(already);
    }
    return () => {
      unsubscribeForeground?.();
      unsubscribePending();
    };
  }, [navigate]);

  // Android back does what the header's back arrow does; on the list it
  // leaves the app.
  useEffect(() => {
    if (Platform.OS !== 'android') return;
    const sub = BackHandler.addEventListener('hardwareBackPress', () => {
      if (route.name === 'home') return false;
      goBack();
      return true;
    });
    return () => sub.remove();
  }, [route, goBack]);

  const title =
    route.name === 'deployment'
      ? route.id
        ? deployments.find(d => d.id === route.id)?.host ?? TITLES.deployment
        : 'New deployment'
      : TITLES[route.name];

  return (
    <View style={styles.container}>
      <StatusBar barStyle={theme.name === 'light' || theme.name === 'sepia' ? 'dark-content' : 'light-content'} />

      {error && loaded && (
        <View style={styles.offlineBanner}>
          <Text style={styles.offlineBannerText} numberOfLines={2}>
            {error}. Retrying…
          </Text>
        </View>
      )}

      <View style={styles.header}>
        {/* The constellation carries on up through the header. */}
        <PixelField />
        {route.name !== 'home' && (
          <Pressable style={styles.headerBack} onPress={goBack} accessibilityLabel="Back" hitSlop={8}>
            <Icon name="chevron-left" size={22} color={theme.text} />
          </Pressable>
        )}
        <View style={styles.headerTextArea}>
          {route.name === 'home' ? (
            // The app's own lettering, on one line, rather than plain text.
            <LogoLine height={22} />
          ) : (
            <Text style={styles.headerTitle} numberOfLines={1}>
              {title}
            </Text>
          )}
          {route.name === 'home' && server ? (
            <Text style={styles.headerSubtitle} numberOfLines={1}>
              {serverLabel(server)}
            </Text>
          ) : null}
        </View>
        {route.name === 'home' && (
          <>
            <Pressable style={styles.headerIconButton} onPress={() => navigate({name: 'domains'})} accessibilityLabel="Domains">
              <Icon name="globe" size={20} color={theme.text} />
            </Pressable>
            <Pressable style={styles.headerIconButton} onPress={() => navigate({name: 'relays'})} accessibilityLabel="Relays">
              <Icon name="server" size={20} color={theme.text} />
            </Pressable>
            <Pressable style={styles.headerIconButton} onPress={() => navigate({name: 'settings'})} accessibilityLabel="Settings">
              <Icon name="gear" size={20} color={theme.text} />
            </Pressable>
          </>
        )}
      </View>

      <View style={styles.body}>
        {route.name === 'home' && <HomeScreen onNavigate={navigate} />}
        {route.name === 'deployment' && <DeploymentScreen key={route.id ?? 'new'} id={route.id} onNavigate={navigate} />}
        {route.name === 'relays' && <RelaysScreen />}
        {route.name === 'domains' && <DomainsScreen />}
        {route.name === 'settings' && <SettingsScreen />}
      </View>
      {/* Modal sheets (Overlay modal), over everything. */}
      <PortalOutlet />
    </View>
  );
}

const styles = StyleSheet.create({
  fill: {flex: 1, alignItems: 'center', justifyContent: 'center'},
  fillBox: {flex: 1},
});

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    container: {flex: 1, backgroundColor: theme.bg},
    offlineBanner: {
      backgroundColor: theme.dangerBg,
      paddingVertical: 8,
      paddingHorizontal: 16,
      borderBottomWidth: 1,
      borderBottomColor: theme.danger,
    },
    offlineBannerText: {color: theme.danger, fontSize: 12, fontWeight: '600', textAlign: 'center'},
    header: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingHorizontal: 12,
      paddingVertical: 12,
      borderBottomWidth: 1,
      borderBottomColor: theme.border,
      gap: 4,
    },
    headerBack: {width: 36, height: 36, alignItems: 'center', justifyContent: 'center', marginLeft: -6},
    headerTextArea: {flex: 1, minWidth: 0},
    headerTitle: {fontSize: 16, fontWeight: '700', color: theme.text},
    headerSubtitle: {fontSize: 11, color: theme.textMuted, marginTop: 3},
    headerIconButton: {width: 36, height: 36, alignItems: 'center', justifyContent: 'center'},
    body: {flex: 1, backgroundColor: theme.bg},
  });
}

// Web: the installable app's service worker and update policy (src/pwa).
// Sessions survive a reload, so a new version switches in (a reload) unless
// someone is looking at the page past its first few seconds, when it could
// interrupt a form being filled in.
const launchedAt = Date.now();
startPwa(() => typeof document !== 'undefined' && document.visibilityState === 'visible' && Date.now() - launchedAt > 8000);

/** Keeps the browser's status bar / title bar in the theme's background (web). */
function ThemeChrome(): null {
  const theme = useTheme();
  useEffect(() => setThemeColor(theme.bg), [theme.bg]);
  return null;
}

/**
 * Android draws the app edge to edge (under the status and navigation bars;
 * required when targeting API 35+), so keep everything clear of them with
 * the real insets, and lift it above the keyboard (adjustResize no longer
 * does that edge to edge). The web build does both in index.html.
 */
function SystemBars({children}: {children: React.ReactNode}): React.JSX.Element {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  // How much of the screen the keyboard covers while it's up (0 when it's
  // down): from its top edge to the bottom of the screen.
  const [keyboard, setKeyboard] = useState(0);
  useEffect(() => {
    if (Platform.OS !== 'android') return;
    const show = Keyboard.addListener('keyboardDidShow', e => {
      const covered = Dimensions.get('screen').height - e.endCoordinates.screenY;
      setKeyboard(covered > 0 ? covered : e.endCoordinates.height + insets.bottom);
    });
    const hide = Keyboard.addListener('keyboardDidHide', () => setKeyboard(0));
    return () => {
      show.remove();
      hide.remove();
    };
  }, [insets.bottom]);
  if (Platform.OS !== 'android') return <>{children}</>;
  return (
    <View
      style={[
        styles.fillBox,
        {
          backgroundColor: theme.bg,
          paddingTop: insets.top,
          paddingBottom: keyboard || insets.bottom,
          paddingLeft: insets.left,
          paddingRight: insets.right,
        },
      ]}>
      {children}
    </View>
  );
}

function App(): React.JSX.Element {
  return (
    <CrashScreen>
      <SafeAreaProvider>
        <ThemeProvider>
          <ThemeChrome />
          <SystemBars>
            <PortalProvider>
              <SplashGate>
                <SessionGate />
              </SplashGate>
            </PortalProvider>
          </SystemBars>
        </ThemeProvider>
      </SafeAreaProvider>
    </CrashScreen>
  );
}

export default App;
