import React, {useRef} from 'react';
import {ActivityIndicator, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {AuthMode, Deployment, RelayState} from '../api';
import {Icon, IconName} from '../components/Icon';
import {SpringyPull} from '../components/SpringyPull';
import {Button, Dot} from '../components/ui';
import {isFallback, transportBadge} from '../link';
import {Route} from '../routes';
import {deploymentHealth, Health, refresh, useStore} from '../store';
import {Theme, useTheme} from '../theme';

export const AUTH_LABEL: Record<AuthMode, string> = {
  none: 'Public',
  login: 'Login',
  basic: 'Basic auth',
  token: 'API tokens',
};

export const AUTH_ICON: Record<AuthMode, IconName> = {
  none: 'globe',
  login: 'lock',
  basic: 'shield',
  token: 'key',
};

export function healthColor(theme: Theme, h: Health): string {
  switch (h) {
    case 'ok':
      return theme.success;
    case 'unreachable':
      return '#d29922';
    case 'offline':
      return theme.danger;
    default:
      return theme.textMuted;
  }
}

/** The deployments on this server: what's exposed where, and whether it's up. */
export function HomeScreen({onNavigate}: {onNavigate: (r: Route) => void}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const loaded = useStore(s => s.loaded);
  const deployments = useStore(s => s.deployments);
  const relays = useStore(s => s.relays);
  const live = useStore(s => s.live);
  const tls = useStore(s => s.info?.tls ?? true);
  const info = useStore(s => s.info);
  const scrollY = useRef(0);

  if (!loaded) {
    return (
      <View style={styles.center}>
        <ActivityIndicator color={theme.textMuted} />
      </View>
    );
  }

  const relayName = (id: string) => relays.find(r => r.id === id)?.name ?? 'unknown relay';

  return (
    <SpringyPull atTop={() => scrollY.current <= 0} onRefresh={refresh}>
      <ScrollView
        style={styles.fill}
        contentContainerStyle={styles.container}
        onScroll={e => (scrollY.current = e.nativeEvent.contentOffset.y)}
        scrollEventThrottle={16}>
        {relays.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="server" size={28} color={theme.textMuted} />
            <Text style={styles.emptyTitle}>Add your first relay</Text>
            <Text style={styles.emptyText}>
              A relay runs on the machine next to what you want to expose. It dials out to this server, so it needs no open ports.
            </Text>
            <Button label="Add a relay" icon="plus" onPress={() => onNavigate({name: 'relays'})} style={styles.emptyButton} />
          </View>
        ) : deployments.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="globe" size={28} color={theme.textMuted} />
            <Text style={styles.emptyTitle}>Nothing exposed yet</Text>
            <Text style={styles.emptyText}>
              A deployment puts a service a relay can reach (like http://127.0.0.1:8080) at its own hostname, with its own certificate and
              sign-in in front.
            </Text>
            <Button
              label="New deployment"
              icon="plus"
              onPress={() => onNavigate({name: 'deployment', id: null})}
              style={styles.emptyButton}
            />
          </View>
        ) : (
          <>
            {deployments.map(d => (
              <DeploymentRow
                key={d.id}
                d={d}
                relay={relayName(d.relayId)}
                health={deploymentHealth(d, live)}
                link={live[d.relayId]}
                fallback={isFallback(info, live[d.relayId])}
                tls={tls}
                onPress={() => onNavigate({name: 'deployment', id: d.id})}
              />
            ))}
            <Button
              label="New deployment"
              icon="plus"
              kind="secondary"
              onPress={() => onNavigate({name: 'deployment', id: null})}
              style={styles.newButton}
            />
          </>
        )}
      </ScrollView>
    </SpringyPull>
  );
}

function DeploymentRow({
  d,
  relay,
  health,
  link,
  fallback,
  tls,
  onPress,
}: {
  d: Deployment;
  relay: string;
  health: {health: Health; detail: string};
  /** the relay's live link, if it's online */
  link: RelayState | undefined;
  /** on the WebSocket although QUIC is offered */
  fallback: boolean;
  tls: boolean;
  onPress: () => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const certNote = tls && d.enabled && d.certStatus !== 'ok' ? (d.certStatus === 'error' ? 'cert error' : 'cert pending') : null;
  return (
    <Pressable
      style={({pressed}) => [styles.row, pressed && styles.rowPressed]}
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={`${d.host}, ${health.detail}`}>
      <View style={styles.rowDot}>
        <Dot color={healthColor(theme, health.health)} />
      </View>
      <View style={styles.rowBody}>
        <Text style={[styles.host, !d.enabled && styles.muted]} numberOfLines={1}>
          {d.host}
        </Text>
        <Text style={styles.target} numberOfLines={1}>
          {d.protocol}://{d.targetHost}:{d.targetPort} · {relay}
        </Text>
        <View style={styles.badges}>
          <View style={styles.badge}>
            <Icon name={AUTH_ICON[d.authMode]} size={12} color={theme.textMuted} />
            <Text style={styles.badgeText}>{AUTH_LABEL[d.authMode]}</Text>
          </View>
          {link && (
            <View style={[styles.badge, fallback && styles.badgeWarn]}>
              <Icon name="globe" size={12} color={fallback ? theme.danger : theme.textMuted} />
              <Text style={[styles.badgeText, fallback && styles.badgeWarnText]}>{transportBadge(link)}</Text>
            </View>
          )}
          {d.ipAllowlist.length > 0 && (
            <View style={styles.badge}>
              <Icon name="shield" size={12} color={theme.textMuted} />
              <Text style={styles.badgeText}>IP allow list</Text>
            </View>
          )}
          {certNote && (
            <View style={[styles.badge, d.certStatus === 'error' && styles.badgeWarn]}>
              <Text style={[styles.badgeText, d.certStatus === 'error' && styles.badgeWarnText]}>{certNote}</Text>
            </View>
          )}
          {health.health !== 'ok' && (
            <Text style={styles.detail} numberOfLines={1}>
              {health.detail}
            </Text>
          )}
        </View>
      </View>
      <Icon name="chevron-right" size={18} color={theme.textMuted} />
    </Pressable>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    center: {flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: theme.bg},
    container: {padding: 12, paddingBottom: 48, maxWidth: 720, width: '100%', alignSelf: 'center'},
    row: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      padding: 14,
      marginBottom: 8,
      borderRadius: 10,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.surface,
    },
    rowPressed: {backgroundColor: theme.surfaceAlt},
    rowDot: {alignSelf: 'flex-start', paddingTop: 6},
    rowBody: {flex: 1, minWidth: 0, gap: 3},
    host: {fontSize: 15, fontWeight: '700', color: theme.text},
    muted: {color: theme.textMuted},
    target: {fontSize: 12, fontFamily: 'monospace', color: theme.textMuted},
    badges: {flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: 6, marginTop: 4},
    badge: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 4,
      paddingHorizontal: 7,
      paddingVertical: 2,
      borderRadius: 5,
      backgroundColor: theme.surfaceAlt,
    },
    badgeText: {fontSize: 11, fontWeight: '600', color: theme.textMuted},
    badgeWarn: {backgroundColor: theme.dangerBg},
    badgeWarnText: {color: theme.danger},
    detail: {fontSize: 11, color: theme.textMuted, flexShrink: 1},
    newButton: {marginTop: 8, alignSelf: 'center'},
    empty: {alignItems: 'center', paddingVertical: 48, paddingHorizontal: 24, gap: 8},
    emptyTitle: {fontSize: 16, fontWeight: '700', color: theme.text, marginTop: 4},
    emptyText: {fontSize: 13, color: theme.textMuted, textAlign: 'center', lineHeight: 19, maxWidth: 360},
    emptyButton: {marginTop: 12},
  });
}
