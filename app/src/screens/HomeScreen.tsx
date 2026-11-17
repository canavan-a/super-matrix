import React, {useRef, useState} from 'react';
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';

import {
  AppliedNixService,
  AuthMode,
  Bridge,
  Deployment,
  NixService,
  Relay,
  RelayState,
} from '../api';
import {Icon, IconName} from '../components/Icon';
import {ManagedBadge} from '../components/Managed';
import {Overlay} from '../components/Overlay';
import {SpringyPull} from '../components/SpringyPull';
import {Button, Chips, Dot} from '../components/ui';
import {isFallback, transportBadge} from '../link';
import {Route} from '../routes';
import {settingsStore, useSettings} from '../settings';
import {
  allApplied,
  applyNixService,
  bridgeHealth,
  deploymentHealth,
  Health,
  locked,
  refresh,
  supports,
  useStore,
} from '../store';
import {phaseColor, phaseLabel, phaseSentence, progressLine} from '../nix/phase';
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
  const nixServices = useStore(s => s.nixServices);
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
  // Nix services, one card per relay each runs on.
  const applied = allApplied(nixServices).sort(
    (a, b) =>
      a.service.name.localeCompare(b.service.name) ||
      a.applied.relayName.localeCompare(b.applied.relayName),
  );
  const nixRow = (x: {service: NixService; applied: AppliedNixService}) => (
    <NixRow
      key={x.applied.id}
      service={x.service}
      a={x.applied}
      onPress={() => onNavigate({name: 'appliedNix', id: x.applied.id})}
    />
  );

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
        ) : deployments.length === 0 &&
          bridges.length === 0 &&
          applied.length === 0 ? (
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
                  const ns = g.relay
                    ? applied.filter(x => x.applied.relayId === g.relay?.id)
                    : [];
                  return (
                    <View key={g.relay?.id ?? 'unknown'} style={styles.group}>
                      <RelayHeader
                        name={g.relay?.name ?? 'Unknown relay'}
                        link={g.relay ? live[g.relay.id] : undefined}
                        count={g.items.length}
                        bridges={bs.length}
                        nix={ns.length}
                      />
                      {g.items.length === 0 &&
                        bs.length === 0 &&
                        ns.length === 0 && (
                          <Text style={styles.groupEmpty}>
                            Nothing on this relay
                          </Text>
                        )}
                      {g.items.map(row)}
                      {bs.map(x => bridgeRow(x.bridge, x.role))}
                      {ns.map(nixRow)}
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
                  {applied.length > 0 && (
                    <SectionHeader
                      icon="snowflake"
                      title="Nix services"
                      hint="Built and run on relays"
                      count={applied.length}
                    />
                  )}
                  {applied.map(nixRow)}
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
        onNavigate={onNavigate}
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

/** "+ New": a deployment, a bridge, or a Nix service run on a relay. */
function NewChooser({
  visible,
  onClose,
  onNavigate,
}: {
  visible: boolean;
  onClose: () => void;
  onNavigate: (r: Route) => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const hasNix = useStore(s => supports(s.info, 'nix-services'));
  const services = useStore(s => s.nixServices);
  const relays = useStore(s => s.relays);
  const schema = useStore(s => s.schema);
  // The Nix steps: pick a service, then a relay to run it on.
  const [step, setStepRaw] = useState<'kind' | 'service' | string>('kind');
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // An error belongs to the step it happened on.
  const setStep = (s: string) => {
    setStepRaw(s);
    setError(null);
  };
  const close = () => {
    setStep('kind');
    onClose();
  };
  const go = (r: Route) => {
    close();
    onNavigate(r);
  };
  const options: {
    name: 'deployment' | 'bridge' | 'nix';
    icon: IconName;
    title: string;
    text: string;
    disabled?: boolean;
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
  if (hasNix) {
    options.push({
      name: 'nix',
      icon: 'snowflake',
      title: 'Nix service',
      text:
        services.length === 0
          ? 'No Nix services are defined yet. Define one under Nix services (the snowflake up top), then run it on a relay from here.'
          : "Run one of your Nix services on a relay: the relay's machine builds it and runs it as a systemd unit.",
      disabled: services.length === 0,
    });
  }
  const service = services.find(s => s.id === step);

  let body: React.JSX.Element;
  if (step === 'kind') {
    body = (
      <>
        <Text style={styles.sheetTitle}>New</Text>
        {options.map(o => (
          <Pressable
            key={o.name}
            style={({pressed}) => [
              styles.choice,
              o.disabled && styles.choiceDisabled,
              pressed && !o.disabled && styles.rowPressed,
            ]}
            disabled={o.disabled}
            onPress={() =>
              o.name === 'nix' ? setStep('service') : go({name: o.name, id: null})
            }
            accessibilityRole="button"
            accessibilityState={{disabled: !!o.disabled}}
            accessibilityLabel={`New ${o.title.toLowerCase()}`}>
            <Icon
              name={o.icon}
              size={22}
              color={o.disabled ? theme.textMuted : theme.primary}
            />
            <View style={styles.rowBody}>
              <Text style={[styles.host, o.disabled && styles.choiceTitleDisabled]}>
                {o.title}
              </Text>
              <Text style={styles.choiceText}>{o.text}</Text>
            </View>
            {!o.disabled && (
              <Icon name="chevron-right" size={18} color={theme.textMuted} />
            )}
          </Pressable>
        ))}
      </>
    );
  } else if (step === 'service' || !service) {
    body = (
      <>
        <SheetBack title="Which Nix service?" onBack={() => setStep('kind')} />
        <ScrollView style={styles.sheetList}>
          {services.map(s => {
            const on = s.applied.filter(a => !a.removing).length;
            return (
              <Pressable
                key={s.id}
                style={({pressed}) => [styles.choice, styles.choiceGap, pressed && styles.rowPressed]}
                onPress={() => setStep(s.id)}
                accessibilityRole="button"
                accessibilityLabel={`Run ${s.name}`}>
                <Icon name="snowflake" size={20} color={theme.primary} />
                <View style={styles.rowBody}>
                  <Text style={styles.host}>{s.name}</Text>
                  <Text style={styles.choiceText}>
                    {s.slug} ·{' '}
                    {on === 0 ? 'not on any relay' : on === 1 ? 'on 1 relay' : `on ${on} relays`}
                  </Text>
                </View>
                <Icon name="chevron-right" size={18} color={theme.textMuted} />
              </Pressable>
            );
          })}
        </ScrollView>
      </>
    );
  } else {
    const on = new Set(service.applied.filter(a => !a.removing).map(a => a.relayId));
    // Still being torn down there: it can't be applied again until it's gone.
    const removing = new Set(service.applied.filter(a => a.removing).map(a => a.relayId));
    const relaysLocked = locked(service.managed, 'relays', schema);
    const sorted = [...relays].sort((a, b) => a.name.localeCompare(b.name));
    body = (
      <>
        <SheetBack title={`Run ${service.name} on`} onBack={() => setStep('service')} />
        {relaysLocked ? (
          <Text style={styles.choiceText}>
            The schema says which relays it runs on.
          </Text>
        ) : (
          <ScrollView style={styles.sheetList}>
            {sorted.map(r => {
              const already = on.has(r.id);
              const tearingDown = !already && removing.has(r.id);
              const ready = !!r.nix?.ready;
              return (
                <View key={r.id} style={[styles.choice, styles.choiceGap]}>
                  <View style={styles.rowBody}>
                    <Text style={styles.host}>{r.name}</Text>
                    <Text style={styles.choiceText} numberOfLines={2}>
                      {already
                        ? 'Already running it'
                        : tearingDown
                        ? 'Being removed from it: apply it again once that finishes'
                        : ready
                        ? `nix ${r.nix?.host?.version ?? ''} on ${r.nix?.host?.system ?? 'its machine'}`
                        : r.nix?.reason ?? 'This server is too old to say'}
                    </Text>
                  </View>
                  {already ? (
                    <Button
                      label="Open"
                      kind="secondary"
                      onPress={() => {
                        const a = service.applied.find(x => x.relayId === r.id && !x.removing);
                        if (a) go({name: 'appliedNix', id: a.id});
                      }}
                    />
                  ) : (
                    <Button
                      label="Apply"
                      kind="secondary"
                      disabled={!ready || tearingDown}
                      busy={busy === r.id}
                      onPress={() => {
                        setBusy(r.id);
                        setError(null);
                        applyNixService(service.id, r.id)
                          .then(a => go({name: 'appliedNix', id: a.id}))
                          .catch(e => setError((e as Error).message))
                          .finally(() => setBusy(null));
                      }}
                    />
                  )}
                </View>
              );
            })}
          </ScrollView>
        )}
        {error ? <Text style={styles.sheetError}>{error}</Text> : null}
      </>
    );
  }

  return (
    <Overlay visible={visible} onClose={close} modal style={styles.sheetWrap}>
      <View style={styles.sheet}>{body}</View>
    </Overlay>
  );
}

/** A sheet step's title, with a way back to the one before. */
function SheetBack({title, onBack}: {title: string; onBack: () => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  return (
    <View style={styles.sheetBack}>
      <Pressable onPress={onBack} accessibilityRole="button" accessibilityLabel="Back" hitSlop={8}>
        <Icon name="chevron-left" size={20} color={theme.text} />
      </Pressable>
      <Text style={[styles.sheetTitle, styles.sheetBackTitle]} numberOfLines={1}>
        {title}
      </Text>
    </View>
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

/** A Nix service on one relay: its phase, and what it's doing if it's not simply running. */
function NixRow({
  service,
  a,
  onPress,
}: {
  service: NixService;
  a: AppliedNixService;
  onPress: () => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  // Stopped on purpose, or on its way out: greyed out, like a disabled row.
  const off = a.removing || (a.desired === 'stopped' && a.phase === 'stopped');
  const detail =
    a.phase === 'building' && a.progress
      ? progressLine(a.progress)
      : a.phase === 'running' && !a.online
      ? 'relay offline'
      : a.phase === 'running' && !a.upToDate && a.serving
      ? 'older build'
      : phaseSentence(a);
  return (
    <Pressable
      style={({pressed}) => [
        styles.row,
        off && styles.rowDisabled,
        pressed && styles.rowPressed,
      ]}
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={`Nix service ${service.name} on ${a.relayName}, ${phaseLabel(a.phase)}`}>
      <View style={styles.rowDot}>
        <Dot color={phaseColor(theme, a)} />
      </View>
      <View style={styles.rowBody}>
        <View style={styles.hostRow}>
          <Text
            style={[styles.host, styles.hostShrink, a.removing && styles.hostDisabled]}
            numberOfLines={1}>
            {service.name}
          </Text>
          <ManagedBadge managed={a.managed ?? service.managed} />
        </View>
        <Text style={styles.target} numberOfLines={1}>
          smx-{a.slug} · {a.relayName}
        </Text>
        <View style={styles.badges}>
          <View style={[styles.badge, styles.badgeBridge]}>
            <Icon name="snowflake" size={12} color={theme.primary} />
            <Text style={[styles.badgeText, styles.badgeBridgeText]}>NIX</Text>
          </View>
          <View style={[styles.badge, a.failing && styles.badgeWarn]}>
            <Text style={[styles.badgeText, a.failing && styles.badgeWarnText]}>
              {phaseLabel(a.phase)}
            </Text>
          </View>
          {detail ? (
            <Text style={styles.detail} numberOfLines={1}>
              {detail}
            </Text>
          ) : null}
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
  nix,
}: {
  name: string;
  link: RelayState | undefined;
  count: number;
  bridges: number;
  nix: number;
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
          nix === 0 ? '' : nix === 1 ? '1 Nix service' : `${nix} Nix services`,
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
    // Greyed out, like a disabled row.
    choiceDisabled: {backgroundColor: theme.bg, borderStyle: 'dashed', opacity: 0.6},
    choiceTitleDisabled: {color: theme.textMuted},
    choiceGap: {marginBottom: 8},
    sheetList: {maxHeight: 420},
    sheetBack: {flexDirection: 'row', alignItems: 'center', gap: 8, marginBottom: 4},
    sheetBackTitle: {marginBottom: 0, flexShrink: 1},
    sheetError: {fontSize: 12, color: theme.danger},
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
