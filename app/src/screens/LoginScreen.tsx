import React, {useEffect, useState} from 'react';
import {BackHandler, KeyboardAvoidingView, Platform, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {api} from '../api';
import {Icon} from '../components/Icon';
import {Notice} from '../components/Notice';
import {Button, Input, Label} from '../components/ui';
import {LogoArt, MarkArt} from '../logo/LogoArt';
import {serverLabel, settingsStore} from '../settings';
import {cancelSignIn, hasSession, signIn, switchServer, useStore} from '../store';
import {Theme, useTheme} from '../theme';

/** The web app is usually served by the server itself: offer its origin. */
function webOrigin(): string | null {
  if (Platform.OS !== 'web' || typeof window === 'undefined') return null;
  return window.location.origin;
}

export function LoginScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const lastServer = useStore(s => s.server);
  const reason = useStore(s => s.signedOutReason);
  const [server, setServer] = useState(lastServer ?? '');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const saved = settingsStore.get().servers;
  // Came here from "Add a server": back returns to that server.
  const returnTo = useStore(s => s.returnTo);
  const canGoBack = !!returnTo && hasSession(returnTo);

  // Android back does the same; with nowhere to go it leaves the app.
  useEffect(() => {
    if (Platform.OS !== 'android' || !canGoBack) return;
    const sub = BackHandler.addEventListener('hardwareBackPress', cancelSignIn);
    return () => sub.remove();
  }, [canGoBack]);

  useEffect(() => {
    if (server) return;
    const origin = webOrigin();
    if (origin) api.health(origin).then(ok => ok && setServer(s => s || origin));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const submit = async () => {
    if (!password) return;
    setBusy(true);
    setError(null);
    try {
      await signIn(server, password);
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <KeyboardAvoidingView style={styles.fill} behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
      {canGoBack && (
        <Pressable style={styles.back} onPress={cancelSignIn} accessibilityLabel="Back" hitSlop={8}>
          <Icon name="chevron-left" size={22} color={theme.text} />
          <Text style={styles.backText} numberOfLines={1}>
            {serverLabel(returnTo!)}
          </Text>
        </Pressable>
      )}
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <View style={styles.brand}>
          <MarkArt height={48} />
          <View style={styles.gap} />
          <LogoArt height={92} />
        </View>

        {reason ? <Notice kind="info" title="Signed out">{reason}</Notice> : null}

        <Label spaced>Server</Label>
        <Input
          value={server}
          onChangeText={setServer}
          placeholder="https://matrix.example.com"
          keyboardType="url"
          textContentType="URL"
          accessibilityLabel="Server address"
        />
        {saved.length > 0 && (
          <View style={styles.saved}>
            {saved.map(url => (
              <Pressable
                key={url}
                style={[styles.savedChip, url === server && styles.savedChipOn]}
                onPress={() => (hasSession(url) ? switchServer(url) : setServer(url))}>
                <Text style={[styles.savedText, url === server && styles.savedTextOn]} numberOfLines={1}>
                  {serverLabel(url)}
                </Text>
              </Pressable>
            ))}
          </View>
        )}

        <Label spaced>Root password</Label>
        <Input
          value={password}
          onChangeText={setPassword}
          secureTextEntry
          placeholder="Set on the server"
          textContentType="password"
          autoComplete="password"
          onSubmitEditing={submit}
          returnKeyType="go"
          accessibilityLabel="Root password"
        />

        {error ? <Notice kind="error" title="Couldn't sign in">{error}</Notice> : null}

        <Button label="Sign in" onPress={submit} busy={busy} disabled={!server.trim() || !password} style={styles.submit} />
        <Text style={styles.footer}>
          The root password is set when the server is installed (SUPERMATRIX_ROOT_PASSWORD_FILE). Signing in keeps a
          session on this device; the password isn't stored.
        </Text>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    back: {flexDirection: 'row', alignItems: 'center', gap: 2, alignSelf: 'flex-start', padding: 12, maxWidth: '100%'},
    backText: {fontSize: 14, fontWeight: '600', color: theme.text, flexShrink: 1},
    container: {padding: 24, paddingTop: 48, maxWidth: 420, width: '100%', alignSelf: 'center'},
    brand: {alignItems: 'center', marginBottom: 20},
    gap: {height: 18},
    saved: {flexDirection: 'row', flexWrap: 'wrap', gap: 6, marginTop: 8},
    savedChip: {paddingHorizontal: 10, paddingVertical: 5, borderRadius: 6, backgroundColor: theme.surfaceAlt, maxWidth: '100%'},
    savedChipOn: {backgroundColor: theme.primary},
    savedText: {fontSize: 12, fontWeight: '600', color: theme.text},
    savedTextOn: {color: theme.primaryText},
    submit: {marginTop: 20},
    footer: {fontSize: 12, color: theme.textMuted, marginTop: 16, lineHeight: 17, textAlign: 'center'},
  });
}
