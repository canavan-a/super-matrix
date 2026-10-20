// Settings (web only): install the app to the home screen / as an app
// window, and switch to a downloaded update. See src/pwa.
import React from 'react';
import {Platform, Pressable, StyleSheet, Text, View} from 'react-native';

import {applyUpdateNow, promptInstall, usePwa} from '../pwa/pwa';
import {Theme, useTheme} from '../theme';

export function InstallCard(): React.JSX.Element | null {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const pwa = usePwa();
  if (Platform.OS !== 'web') return null;
  return (
    <View>
      <Text style={styles.title2}>App</Text>
      {pwa.updateReady ? (
        <View style={styles.row}>
          <Text style={[styles.text, styles.flex]}>
            A new version is ready. It switches in on your next launch, or now
            (you'll unlock again).
          </Text>
          <Pressable
            style={[styles.btn, {backgroundColor: theme.primary}]}
            onPress={applyUpdateNow}>
            <Text style={[styles.btnText, {color: theme.primaryText}]}>
              Restart to update
            </Text>
          </Pressable>
        </View>
      ) : null}
      {pwa.standalone ? (
        <Text style={styles.hint}>
          Installed ✓ Updates download by themselves and switch in on launch.
        </Text>
      ) : pwa.canPrompt ? (
        <View style={styles.row}>
          <Text style={[styles.text, styles.flex]}>
            Install Super Matrix as an app, with its own icon and window.
          </Text>
          <Pressable style={styles.btn} onPress={() => promptInstall()}>
            <Text style={styles.btnText}>Install app</Text>
          </Pressable>
        </View>
      ) : pwa.ios ? (
        <>
          <Text style={styles.text}>
            Install Super Matrix on your home screen: tap the Share button, then
            “Add to Home Screen”. It opens full screen, like an app.
          </Text>
          <Text style={styles.hint}>
            The installed app keeps its own storage, separate from Safari, so
            you'll set it up there with your recovery phrase or key file.
          </Text>
        </>
      ) : (
        <Text style={styles.text}>
          To install Super Matrix as an app, use your browser's menu: “Install
          app” or “Add to Home screen”.
        </Text>
      )}
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    flex: {flex: 1},
    title2: {
      fontSize: 15,
      fontWeight: '700',
      marginBottom: 8,
      color: theme.text,
    },
    text: {fontSize: 13, color: theme.text, lineHeight: 19},
    hint: {fontSize: 12, color: theme.textMuted, marginTop: 6},
    row: {flexDirection: 'row', alignItems: 'center', gap: 10, marginBottom: 8},
    btn: {
      paddingHorizontal: 12,
      paddingVertical: 8,
      borderRadius: 6,
      backgroundColor: theme.surfaceAlt,
    },
    btnText: {fontSize: 12, fontWeight: '600', color: theme.text},
  });
}
