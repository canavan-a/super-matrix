import React, {useRef, useState} from 'react';
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';

import {AuthMode, Bridge, Deployment, Relay, RelayState} from '../api';
import {Icon, IconName} from '../components/Icon';
import {ManagedBadge} from '../components/Managed';
import {Overlay} from '../components/Overlay';
import {SpringyPull} from '../components/SpringyPull';
import {Button, Chips, Dot} from '../components/ui';
import {isFallback, transportBadge} from '../link';
import {Route} from '../routes';
import {settingsStore, useSettings} from '../settings';
import {
  bridgeHealth,
  deploymentHealth,
  Health,
  refresh,
  useStore,
} from '../store';
import {Theme, useTheme} from '../theme';
import {bridgeLine} from './BridgeScreen';

export const AUTH_LABEL: Record<AuthMode, string> = {
  none: 'Public',
  login: 'Login',
  otp: 'Email code',
  basic: 'Basic auth',
  token: 'API tokens',
};

export const AUTH_ICON: Record<AuthMode, IconName> = {
  none: 'globe',
  login: 'lock',
  otp: 'mail',
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

/** What's on this server: deployments (public hostnames) and bridges (ports between relays), and whether each is up. */
export function HomeScreen({
  onNavigate,
}: {
  onNavigate: (r: Route) => void;
}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const loaded = useStore(s => s.loaded);
  const deployments = useStore(s => s.deployments);
  const bridges = useStore(s => s.bridges);
  const relays = useStore(s => s.relays);
  const live = useStore(s => s.live);
  const publicLive = useStore(s => s.publicLive);
  const tls = useStore(s => s.info?.tls ?? true);
  const info = useStore(s => s.info);
  const scrollY = useRef(0);
  const groupBy = useSettings().homeGroupBy;
  const [choosing, setChoosing] = useState(false);

  if (!loaded) {
    return (
      <View style={styles.center}>
        <ActivityIndicator color={theme.textMuted} />
      </View>
    );
  }

  const relayName = (id: string) =>
    relays.find(r => r.id === id)?.name ?? 'unknown relay';
  const row = (d: Deployment) => (
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
  );
  const bridgeRow = (b: Bridge, role?: 'from' | 'to') => (
    <BridgeRow
      key={b.id + (role ?? '')}
      b={b}
      role={role}
      line={bridgeLine(b, relayName)}
      health={bridgeHealth(b, relays, live, publicLive)}
      onPress={() => onNavigate({name: 'bridge', id: b.id})}
    />
  );
  const choose = (name: 'deployment' | 'bridge') => {
    setChoosing(false);
    onNavigate({name, id: null});
  };

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
              A relay runs on the machine next to what you want to expose. It
              dials out to this server, so it needs no open ports.
            </Text>
            <Button
              label="Add a relay"
              icon="plus"
              onPress={() => onNavigate({name: 'relays'})}
              style={styles.emptyButton}
            />
          </View>
        ) : deployments.length === 0 && bridges.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="globe" size={28} color={theme.textMuted} />
            <Text style={styles.emptyTitle}>Nothing exposed yet</Text>
            <Text style={styles.emptyText}>
              A deployment puts a service a relay can reach (like
              http://127.0.0.1:8080) at its own hostname, with its own
              certificate and sign-in in front. A bridge carries a TCP or UDP
              port (like Postgres on 5432) from one relay's network to another.
            </Text>
            <Button
              label="New"
              icon="plus"
              onPress={() => setChoosing(true)}
              style={styles.emptyButton}
            />
          </View>
        ) : (
          <>
            <Chips
              options={[
                {value: 'none', label: 'All'},
                {value: 'relay', label: 'By relay'},
              ]}
              value={groupBy}
              onChange={v => settingsStore.update({homeGroupBy: v})}
            />
            <View style={styles.list}>
              {groupBy === 'relay' ? (
                groupByRelay(deployments, relays).map(g => {
                  const bs = g.relay ? bridgesOf(g.relay.id, bridges) : [];
                  return (
                    <View key={g.relay?.id ?? 'unknown'} style={styles.group}>
                      <RelayHeader
                        name={g.relay?.name ?? 'Unknown relay'}
                        link={g.relay ? live[g.relay.id] : undefined}
                        count={g.items.length}
                        bridges={bs.length}
                      />
                      {g.items.length === 0 && bs.length === 0 && (
                        <Text style={styles.groupEmpty}>
                          Nothing on this relay
                        </Text>
                      )}
                      {g.items.map(row)}
                      {bs.map(x => bridgeRow(x.bridge, x.role))}
                    </View>
                  );
                })
              ) : (
                <>
                  {deployments.length > 0 && (
                    <SectionHeader
                      icon="globe"
                      title="Deployments"
                      hint="Public hostnames"
                      count={deployments.length}
                    />
                  )}
                  {deployments.map(row)}
                  {bridges.length > 0 && (
                    <SectionHeader
                      icon="bridge"
                      title="Bridges"
                      hint="TCP and UDP ports"
                      count={bridges.length}
                    />
                  )}
                  {bridges.map(b => bridgeRow(b))}
                </>
              )}
            </View>
            <Button
              label="New"
              icon="plus"
              kind="secondary"
              onPress={() => setChoosing(true)}
              style={styles.newButton}
            />
          </>
        )}
      </ScrollView>
      <NewChooser
        visible={choosing}
        onClose={() => setChoosing(false)}
        onChoose={choose}
      />
    </SpringyPull>
  );
}

/** The bridges that listen on a relay ("from") or lead to it ("to"). */
export function bridgesOf(
  relayId: string,
  bridges: Bridge[],
): {bridge: Bridge; role: 'from' | 'to'}[] {
  const out: {bridge: Bridge; role: 'from' | 'to'}[] = [];
  for (const b of bridges) {
    if (!b.public && b.fromRelayId === relayId)
      out.push({bridge: b, role: 'from'});
    if (b.toRelayId === relayId) out.push({bridge: b, role: 'to'});
  }
  return out;
}

/** "+ New": a deployment or a bridge. */
function NewChooser({
  visible,
  onClose,
  onChoose,
}: {
  visible: boolean;
  onClose: () => void;
  onChoose: (name: 'deployment' | 'bridge') => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const options: {
    name: 'deployment' | 'bridge';
    icon: IconName;
    title: string;
    text: string;
  }[] = [
    {
      name: 'deployment',
      icon: 'globe',
      title: 'Deployment',
      text: 'A service at its own public hostname, with a certificate and sign-in in front. HTTP, HTTPS or SSH.',
    },
    {
      name: 'bridge',
      icon: 'bridge',
      title: 'Bridge',
      text: "A TCP or UDP port that leads to a service on a relay's network: on another relay's machine, or open to the internet on this server.",
    },
  ];
  return (
    <Overlay visible={visible} onClose={onClose} modal style={styles.sheetWrap}>
      <View style={styles.sheet}>
        <Text style={styles.sheetTitle}>New</Text>
        {options.map(o => (
          <Pressable
            key={o.name}
            style={({pressed}) => [styles.choice, pressed && styles.rowPressed]}
            onPress={() => onChoose(o.name)}
            accessibilityRole="button"
            accessibilityLabel={`New ${o.title.toLowerCase()}`}>
            <Icon name={o.icon} size={22} color={theme.primary} />
            <View style={styles.rowBody}>
              <Text style={styles.host}>{o.title}</Text>
              <Text style={styles.choiceText}>{o.text}</Text>
            </View>
            <Icon name="chevron-right" size={18} color={theme.textMuted} />
          </Pressable>
        ))}
      </View>
    </Overlay>
  );
}

function SectionHeader({
  icon,
  title,
  hint,
  count,
}: {
  icon: IconName;
  title: string;
  hint: string;
  count: number;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  return (
    <View style={styles.groupHeader} accessibilityRole="header">
      <Icon name={icon} size={14} color={theme.textMuted} />
      <Text style={styles.groupName}>{title}</Text>
      <Text style={styles.groupMeta} numberOfLines={1}>
        {hint} · {count}
      </Text>
    </View>
  );
}

function BridgeRow({
  b,
  role,
  line,
  health,
  onPress,
}: {
  b: Bridge;
  /** under a relay: whether the bridge listens on it or leads to it */
  role?: 'from' | 'to';
  line: string;
  health: {health: Health; detail: string};
  onPress: () => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const kind = b.public ? 'PUBLIC BRIDGE' : 'BRIDGE';
  return (
    <Pressable
      style={({pressed}) => [
        styles.row,
        !b.enabled && styles.rowDisabled,
        pressed && styles.rowPressed,
      ]}
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={`Bridge ${b.name}, ${
        b.enabled ? health.detail : 'disabled'
      }`}>
      <View style={styles.rowDot}>
        <Dot color={healthColor(theme, health.health)} />
      </View>
      <View style={styles.rowBody}>
        <View style={styles.hostRow}>
          <Text
            style={[styles.host, styles.hostShrink, !b.enabled && styles.hostDisabled]}
            numberOfLines={1}>
            {role === 'from' ? '→ ' : role === 'to' ? '← ' : ''}
            {b.name}
          </Text>
          <ManagedBadge managed={b.managed} />
        </View>
        <Text style={styles.target} numberOfLines={1}>
          {line} · {b.protocol}
        </Text>
        <View style={styles.badges}>
          <View style={[styles.badge, styles.badgeBridge]}>
            <Icon name="bridge" size={12} color={theme.primary} />
            <Text style={[styles.badgeText, styles.badgeBridgeText]}>
              {kind}
            </Text>
          </View>
          {!b.enabled ? (
            <View style={[styles.badge, styles.badgeDisabled]}>
              <Icon name="power-off" size={12} color={theme.text} />
              <Text style={styles.badgeDisabledText}>DISABLED</Text>
            </View>
          ) : (
            <>
              {b.public && (b.ipAllowlist.length > 0 || b.ipGroups.length > 0) && (
                <View style={styles.badge}>
                  <Icon name="shield" size={12} color={theme.textMuted} />
                  <Text style={styles.badgeText}>IP allow list</Text>
                </View>
              )}
              {health.health !== 'ok' && (
                <Text style={styles.detail} numberOfLines={1}>
                  {health.detail}
                </Text>
              )}
            </>
          )}
        </View>
      </View>
      <Icon name="chevron-right" size={18} color={theme.textMuted} />
    </Pressable>
  );
}

/** Deployments under each relay, relays by name; relays with none get an empty group, and deployments on a relay that's gone come last. */
export function groupByRelay(
  deployments: Deployment[],
  relays: Relay[],
): {relay: Relay | null; items: Deployment[]}[] {
  const groups = [...relays]
    .sort((a, b) => a.name.localeCompare(b.name))
    .map(relay => ({
      relay: relay as Relay | null,
      items: deployments.filter(d => d.relayId === relay.id),
    }));
  const orphans = deployments.filter(
    d => !relays.some(r => r.id === d.relayId),
  );
  if (orphans.length > 0) groups.push({relay: null, items: orphans});
  return groups;
}

function RelayHeader({
  name,
  link,
  count,
  bridges,
}: {
  name: string;
  link: RelayState | undefined;
  count: number;
  bridges: number;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  return (
    <View style={styles.groupHeader} accessibilityRole="header">
      <Dot color={link ? theme.success : theme.textMuted} />
      <Text style={styles.groupName} numberOfLines={1}>
        {name}
      </Text>
      <Text style={styles.groupMeta} numberOfLines={1}>
        {[
          link ? transportBadge(link) : 'offline',
          count === 1 ? '1 deployment' : `${count} deployments`,
          bridges === 0
            ? ''
            : bridges === 1
            ? '1 bridge'
            : `${bridges} bridges`,
        ]
          .filter(Boolean)
          .join(' · ')}
      </Text>
    </View>
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
  const certNote =
    tls && d.enabled && d.certStatus !== 'ok'
      ? d.certStatus === 'error'
        ? 'cert error'
        : 'cert pending'
      : null;
  return (
    <Pressable
      style={({pressed}) => [
        styles.row,
        !d.enabled && styles.rowDisabled,
        pressed && styles.rowPressed,
      ]}
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={
        d.enabled ? `${d.host}, ${health.detail}` : `${d.host}, disabled`
      }>
      <View style={styles.rowDot}>
        <Dot color={healthColor(theme, health.health)} />
      </View>
      <View style={styles.rowBody}>
        <View style={styles.hostRow}>
          <Text
            style={[styles.host, styles.hostShrink, !d.enabled && styles.hostDisabled]}
            numberOfLines={1}>
            {d.host}
          </Text>
          <ManagedBadge managed={d.managed} />
        </View>
        <Text style={styles.target} numberOfLines={1}>
          {d.protocol}://{d.targetHost}:{d.targetPort} · {relay}
        </Text>
        {!d.enabled ? (
          <View style={styles.badges}>
            <View style={[styles.badge, styles.badgeDisabled]}>
              <Icon name="power-off" size={12} color={theme.text} />
              <Text style={styles.badgeDisabledText}>DISABLED</Text>
            </View>
            <Text style={styles.detail} numberOfLines={1}>
              Visitors see "Service disabled"
            </Text>
          </View>
        ) : (
          <View style={styles.badges}>
            <View style={styles.badge}>
              <Icon
                name={AUTH_ICON[d.authMode]}
                size={12}
                color={theme.textMuted}
              />
              <Text style={styles.badgeText}>{AUTH_LABEL[d.authMode]}</Text>
            </View>
            {link && (
              <View style={[styles.badge, fallback && styles.badgeWarn]}>
                <Icon
                  name="globe"
                  size={12}
                  color={fallback ? theme.warning : theme.textMuted}
                />
                <Text
                  style={[styles.badgeText, fallback && styles.badgeWarnText]}>
                  {transportBadge(link)}
                </Text>
              </View>
            )}
            {(d.ipAllowlist.length > 0 || d.ipGroups.length > 0) && (
              <View style={styles.badge}>
                <Icon name="shield" size={12} color={theme.textMuted} />
                <Text style={styles.badgeText}>IP allow list</Text>
              </View>
            )}
            {certNote && (
              <View
                style={[
                  styles.badge,
                  d.certStatus === 'error' && styles.badgeWarn,
                ]}>
                <Text
                  style={[
                    styles.badgeText,
                    d.certStatus === 'error' && styles.badgeWarnText,
                  ]}>
                  {certNote}
                </Text>
              </View>
            )}
            {health.health !== 'ok' && (
              <Text style={styles.detail} numberOfLines={1}>
                {health.detail}
              </Text>
            )}
          </View>
        )}
      </View>
      <Icon name="chevron-right" size={18} color={theme.textMuted} />
    </Pressable>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    center: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      backgroundColor: theme.bg,
    },
    container: {
      padding: 12,
      paddingBottom: 48,
      maxWidth: 720,
      width: '100%',
      alignSelf: 'center',
    },
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
    // Greyed out: flat background, dashed border, faded.
    rowDisabled: {
      backgroundColor: theme.bg,
      borderStyle: 'dashed',
      opacity: 0.6,
    },
    hostDisabled: {color: theme.textMuted, textDecorationLine: 'line-through'},
    badgeDisabled: {backgroundColor: theme.border},
    badgeDisabledText: {
      fontSize: 11,
      fontWeight: '800',
      letterSpacing: 0.6,
      color: theme.text,
    },
    rowDot: {alignSelf: 'flex-start', paddingTop: 6},
    rowBody: {flex: 1, minWidth: 0, gap: 3},
    host: {fontSize: 15, fontWeight: '700', color: theme.text},
    hostRow: {flexDirection: 'row', alignItems: 'center', gap: 6},
    hostShrink: {flexShrink: 1},
    target: {fontSize: 12, fontFamily: 'monospace', color: theme.textMuted},
    badges: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'center',
      gap: 6,
      marginTop: 4,
    },
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
    badgeWarn: {backgroundColor: theme.warningBg},
    badgeWarnText: {color: theme.warning},
    badgeBridge: {backgroundColor: theme.surfaceAlt},
    badgeBridgeText: {
      color: theme.primary,
      fontWeight: '800',
      letterSpacing: 0.5,
    },
    sheetWrap: {justifyContent: 'center', alignItems: 'center', padding: 16},
    sheet: {
      width: '100%',
      maxWidth: 440,
      padding: 16,
      gap: 8,
      borderRadius: 12,
      backgroundColor: theme.bg,
      borderWidth: 1,
      borderColor: theme.border,
    },
    sheetTitle: {
      fontSize: 16,
      fontWeight: '700',
      color: theme.text,
      marginBottom: 4,
    },
    choice: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 12,
      padding: 14,
      borderRadius: 10,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.surface,
    },
    choiceText: {fontSize: 12, color: theme.textMuted, lineHeight: 17},
    detail: {fontSize: 11, color: theme.textMuted, flexShrink: 1},
    newButton: {marginTop: 8, alignSelf: 'center'},
    list: {marginTop: 12},
    group: {marginBottom: 12},
    groupHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 8,
      paddingHorizontal: 4,
      paddingBottom: 8,
    },
    groupName: {
      fontSize: 13,
      fontWeight: '700',
      color: theme.text,
      flexShrink: 1,
    },
    groupMeta: {fontSize: 12, color: theme.textMuted, flexShrink: 1},
    groupEmpty: {
      fontSize: 12,
      color: theme.textMuted,
      paddingHorizontal: 4,
      paddingBottom: 4,
    },
    empty: {
      alignItems: 'center',
      paddingVertical: 48,
      paddingHorizontal: 24,
      gap: 8,
    },
    emptyTitle: {
      fontSize: 16,
      fontWeight: '700',
      color: theme.text,
      marginTop: 4,
    },
    emptyText: {
      fontSize: 13,
      color: theme.textMuted,
      textAlign: 'center',
      lineHeight: 19,
      maxWidth: 360,
    },
    emptyButton: {marginTop: 12},
  });
}
