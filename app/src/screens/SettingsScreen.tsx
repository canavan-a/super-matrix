import React, {useEffect, useState} from 'react';
import {Alert, Platform, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {compareVersions, DowngradeBlockedError, fetchReleases, getVersionInfo, onDownloadProgress, Release, runUpdate} from '../appUpdater';
import {isSideload} from '../distribution';
import {InstallCard} from '../components/InstallCard';
import {Button, Divider, Hint, Label, Title, ToggleRow} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {settingsStore} from '../settings';
import {forgetServer, hasSession, showSignIn, signOut, switchServer, useStore} from '../store';
import {THEME_OPTIONS, Theme, useThemeSetting} from '../theme';

export function SettingsScreen(): React.JSX.Element {
  const {theme, themeName, setThemeName} = useThemeSetting();
  const styles = makeStyles(theme);
  const [s, setS] = useState(settingsStore.get());
  useEffect(() => settingsStore.subscribe(setS), []);
  const server = useStore(st => st.server);
  const info = useStore(st => st.info);
  const connected = useStore(st => st.connected);
  const domainCount = useStore(st => st.domains.length);

  // ---- app updates (Android sideload), same flow as super-badger ----
  const [version, setVersion] = useState<string>();
  const [releases, setReleases] = useState<Release[]>([]);
  const [updateStatus, setUpdateStatus] = useState<string>();
  const [updateBusy, setUpdateBusy] = useState(false);
  const [checkingUpdates, setCheckingUpdates] = useState(false);

  useEffect(() => {
    if (Platform.OS !== 'android' || !isSideload) return;
    getVersionInfo()
      .then(v => setVersion(`${v.versionName} (${v.versionCode})`))
      .catch(() => setVersion('unknown'));
    checkForUpdates();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // A download runs in the background whatever this screen does; while it's
  // open it shows the percentage too.
  const [downloading, setDownloading] = useState(false);
  useEffect(() => {
    if (!downloading) return;
    return onDownloadProgress(({bytesDownloaded, bytesTotal}) => {
      const pct = bytesTotal > 0 ? Math.round((bytesDownloaded / bytesTotal) * 100) : 0;
      if (bytesTotal > 0 && bytesDownloaded >= bytesTotal) {
        setUpdateStatus('Downloaded. Opening the installer…');
        setDownloading(false);
      } else {
        setUpdateStatus(`Downloading in the background… ${pct}%. You can leave this screen.`);
      }
    });
  }, [downloading]);

  const currentVersionName = version?.split(' ')[0];
  const latestRelease = releases[0];
  const updateAvailable = !!latestRelease && !!currentVersionName && compareVersions(latestRelease.version, currentVersionName) > 0;

  const checkForUpdates = async () => {
    setCheckingUpdates(true);
    setUpdateStatus(undefined);
    try {
      setReleases(await fetchReleases());
    } catch (e) {
      setUpdateStatus(`Check failed: ${String(e)}`);
    } finally {
      setCheckingUpdates(false);
    }
  };

  const installUpdate = async () => {
    const target = latestRelease;
    if (!target) return;
    setUpdateBusy(true);
    setUpdateStatus('Starting…');
    try {
      await runUpdate(target);
      setDownloading(true);
      setUpdateStatus(
        "Downloading in the background. Watch the notification; the installer opens when it's done, even if you leave the app.",
      );
    } catch (e) {
      if (e instanceof DowngradeBlockedError) {
        Alert.alert('Downgrade blocked', 'Uninstall the app first to install an older version.');
        setUpdateStatus(undefined);
      } else {
        setUpdateStatus(`Update failed: ${String(e)}`);
      }
    } finally {
      setUpdateBusy(false);
    }
  };

  const others = s.servers.filter(u => u !== server);

  return (
    <ScrollView style={styles.scroll} contentContainerStyle={styles.container}>
      <Title>Server</Title>
      <Text style={styles.mono} selectable>
        {server}
      </Text>
      {info && (
        <Hint>
          {connected ? 'Connected' : 'Reconnecting…'} · v{info.version} · {domainCount} domain{domainCount === 1 ? '' : 's'} ·{' '}
          {info.tls ? "Let's Encrypt certificates" : 'TLS off'}
        </Hint>
      )}
      <View style={styles.actions}>
        <Button
          label="Sign out"
          kind="secondary"
          icon="logout"
          onPress={() =>
            platformConfirm('Sign out?', `You'll need the root password to manage ${server} again.`, 'Sign out', () => signOut(), false)
          }
        />
        <Button label="Add a server" kind="secondary" icon="plus" onPress={showSignIn} />
      </View>

      {others.length > 0 && (
        <>
          <Label spaced>Other servers</Label>
          {others.map(url => (
            <View key={url} style={styles.serverRow}>
              <Text style={styles.serverUrl} numberOfLines={1}>
                {url.replace(/^https?:\/\//, '')}
              </Text>
              <Pressable style={styles.small} onPress={() => switchServer(url)}>
                <Text style={styles.smallText}>{hasSession(url) ? 'Open' : 'Sign in'}</Text>
              </Pressable>
              <Pressable
                style={styles.small}
                onPress={() => platformConfirm('Forget server?', `${url} is removed from this device.`, 'Forget', () => forgetServer(url))}>
                <Text style={styles.smallText}>Forget</Text>
              </Pressable>
            </View>
          ))}
        </>
      )}

      <Divider />
      <Title>Appearance</Title>
      <View style={styles.themeRow}>
        {THEME_OPTIONS.map(opt => (
          <Pressable
            key={opt.name}
            style={[styles.themeSwatch, themeName === opt.name && styles.themeSwatchActive]}
            onPress={() => setThemeName(opt.name)}>
            <View style={styles.themeSwatchColors}>
              <View style={[styles.themeSwatchDot, {backgroundColor: THEME_PREVIEW[opt.name].bg}]} />
              <View style={[styles.themeSwatchDot, {backgroundColor: THEME_PREVIEW[opt.name].primary}]} />
            </View>
            <Text style={styles.themeSwatchLabel}>{opt.label}</Text>
          </Pressable>
        ))}
      </View>

      <ToggleRow
        label="Launch animation"
        hint={
          'The animated logo shown when the app opens. Takes effect next launch.' +
          (s.splashAutoDisabled
            ? " It was turned off automatically because it didn't finish on two launches in a row. Turn it back on to try again."
            : '')
        }
        value={s.launchSplash}
        onChange={v => settingsStore.update({launchSplash: v, splashStrikes: 0, splashAutoDisabled: false})}
      />
      {Platform.OS === 'android' && (
        <ToggleRow
          label="Match app icon to theme"
          hint="Changes the launcher icon to the one for your theme when you leave the app. Some launchers take a moment to refresh."
          value={s.themedIcon}
          onChange={v => settingsStore.update({themedIcon: v})}
        />
      )}

      {Platform.OS === 'web' && (
        <>
          <Divider />
          <InstallCard />
        </>
      )}

      {Platform.OS === 'android' && isSideload && (
        <>
          <Divider />
          <Title>App updates</Title>
          <Pressable onPress={checkForUpdates} disabled={checkingUpdates}>
            <Hint>
              {checkingUpdates
                ? 'Checking…'
                : latestRelease
                ? updateAvailable
                  ? `Update available: v${latestRelease.version}`
                  : `Up to date (v${latestRelease.version})`
                : `Current version: ${version ?? '…'}`}
            </Hint>
          </Pressable>
          {latestRelease && (
            <Button
              label={updateBusy ? 'Working…' : updateAvailable ? `Update to v${latestRelease.version}` : 'Reinstall current version'}
              onPress={installUpdate}
              busy={updateBusy}
              style={styles.spaced}
            />
          )}
          {updateStatus && <Hint>{updateStatus}</Hint>}
        </>
      )}
    </ScrollView>
  );
}

// Small fixed swatch colors for the theme picker — deliberately independent
// of `useTheme()` (which only ever reflects the *currently active* theme):
// the picker needs to show every option's colors at once.
const THEME_PREVIEW: Record<string, {bg: string; primary: string}> = {
  light: {bg: '#ffffff', primary: '#0969da'},
  dark: {bg: '#0d1117', primary: '#2f81f7'},
  slate: {bg: '#1e2530', primary: '#5aa9e6'},
  sepia: {bg: '#f4ecd8', primary: '#a0651b'},
  ember: {bg: '#050403', primary: '#8a3620'},
};

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    scroll: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 560, width: '100%', alignSelf: 'center'},
    mono: {fontFamily: 'monospace', fontSize: 14, color: theme.text},
    actions: {flexDirection: 'row', flexWrap: 'wrap', gap: 8, marginTop: 12},
    spaced: {marginTop: 14},
    serverRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 8,
      paddingVertical: 8,
      borderBottomWidth: 1,
      borderBottomColor: theme.border,
    },
    serverUrl: {flex: 1, fontSize: 14, fontWeight: '600', color: theme.text},
    small: {paddingHorizontal: 12, paddingVertical: 6, borderRadius: 6, backgroundColor: theme.surfaceAlt},
    smallText: {fontWeight: '600', fontSize: 13, color: theme.text},
    themeRow: {flexDirection: 'row', flexWrap: 'wrap', gap: 10, marginBottom: 8},
    themeSwatch: {
      alignItems: 'center',
      gap: 6,
      padding: 10,
      borderRadius: 10,
      borderWidth: 2,
      borderColor: theme.border,
      backgroundColor: theme.surface,
    },
    themeSwatchActive: {borderColor: theme.primary},
    themeSwatchColors: {
      flexDirection: 'row',
      borderRadius: 14,
      overflow: 'hidden',
      borderWidth: 1,
      borderColor: theme.border,
    },
    themeSwatchDot: {width: 20, height: 28},
    themeSwatchLabel: {fontSize: 12, fontWeight: '600', color: theme.text},
  });
}
