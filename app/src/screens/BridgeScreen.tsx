import React, {useEffect, useState} from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {
  Bridge,
  BridgeInput,
  BridgeProtocol,
  DomainDns,
  Relay,
  RelayState,
} from '../api';
import {GroupPicker} from '../components/GroupPicker';
import {Notice} from '../components/Notice';
import {
  Button,
  Chips,
  CopyBox,
  Divider,
  Dot,
  Hint,
  Input,
  Label,
  Title,
  ToggleRow,
} from '../components/ui';
import {transportBadge} from '../link';
import {platformConfirm} from '../platformConfirm';
import {Route} from '../routes';
import {
  bridgeHealth,
  checkBridgeDns,
  deleteBridge,
  saveBridge,
  supportsBridges,
  useStore,
} from '../store';
import {Theme, useTheme} from '../theme';
import {healthColor} from './HomeScreen';
import {ManagedNotice, useLock, useResourceLocked, useStrict} from '../components/Managed';

/** "public" stands for the server itself as the listening side. */
export const PUBLIC = 'public';

export interface BridgeForm {
  name: string;
  /** "" keeps the current slug, or on create has one made from the name */
  slug: string;
  protocol: BridgeProtocol;
  enabled: boolean;
  /** a relay id, or PUBLIC */
  from: string;
  listenHost: string;
  listenPort: string;
  toRelayId: string;
  targetHost: string;
  targetPort: string;
  udpIdleS: number;
  /** one CIDR or address per line (public bridges) */
  ipAllowlist: string;
  /** public bridges: IDs of the IP groups the allow list includes */
  ipGroups: string[];
  /** public bridges: the domain for its DNS name ("" for none), and the label under a wildcard one */
  domainId: string;
  subdomain: string;
}

export function bridgeFormFrom(b: Bridge | null): BridgeForm {
  return {
    name: b?.name ?? '',
    slug: b?.slug ?? '',
    protocol: b?.protocol ?? 'tcp',
    enabled: b?.enabled ?? true,
    from: b ? (b.public ? PUBLIC : b.fromRelayId) : '',
    listenHost: b?.listenHost || '127.0.0.1',
    listenPort: b ? String(b.listenPort) : '',
    toRelayId: b?.toRelayId ?? '',
    targetHost: b?.targetHost ?? '127.0.0.1',
    targetPort: b ? String(b.targetPort) : '',
    udpIdleS: b?.udpIdleS ?? 300,
    ipAllowlist: (b?.ipAllowlist ?? []).join('\n'),
    ipGroups: b?.ipGroups ?? [],
    domainId: b?.domainId ?? '',
    subdomain: b?.subdomain ?? '',
  };
}

export function bridgeFormToInput(f: BridgeForm): BridgeInput {
  const pub = f.from === PUBLIC;
  return {
    name: f.name.trim(),
    ...(f.slug.trim() ? {slug: f.slug.trim().toLowerCase()} : {}),
    protocol: f.protocol,
    enabled: f.enabled,
    public: pub,
    ...(pub
      ? {}
      : {fromRelayId: f.from, listenHost: f.listenHost.trim() || '127.0.0.1'}),
    listenPort: Number(f.listenPort || f.targetPort),
    toRelayId: f.toRelayId,
    targetHost: f.targetHost.trim(),
    targetPort: Number(f.targetPort || f.listenPort),
    udpIdleS: f.udpIdleS,
    ipAllowlist: pub
      ? f.ipAllowlist
          .split(/[\n,]/)
          .map(s => s.trim())
          .filter(Boolean)
      : [],
    ipGroups: pub ? f.ipGroups : [],
    ...(pub
      ? {domainId: f.domainId, subdomain: f.subdomain.trim().toLowerCase()}
      : {}),
  };
}

const IDLE_OPTIONS = [
  {value: 60, label: '1 min'},
  {value: 300, label: '5 min'},
  {value: 900, label: '15 min'},
  {value: 3600, label: '1 hour'},
];

function isLoopback(host: string): boolean {
  const h = host.trim();
  return h === '' || h === '::1' || h === 'localhost' || h.startsWith('127.');
}

/** One bridge: a TCP or UDP port on one side that leads to a service on a relay's network. */
export function BridgeScreen({
  id,
  onNavigate,
}: {
  id: string | null;
  onNavigate: (r: Route) => void;
}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const existing = useStore(s =>
    id ? s.bridges.find(b => b.id === id) ?? null : null,
  );
  const relays = useStore(s => s.relays);
  const live = useStore(s => s.live);
  const publicLive = useStore(s => s.publicLive);
  const info = useStore(s => s.info);
  const domains = useStore(s => s.domains);
  const [form, setForm] = useState<BridgeForm>(() => bridgeFormFrom(existing));
  const lock = useLock(existing?.managed);
  const pinned = useResourceLocked(existing?.managed);
  const strict = useStrict('bridges');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [advanced, setAdvanced] = useState(
    () => !isLoopback(existing?.listenHost ?? ''),
  );

  // Deleted elsewhere: back to the list.
  useEffect(() => {
    if (id && !existing) onNavigate({name: 'home'});
  }, [id, existing, onNavigate]);

  const set = (patch: Partial<BridgeForm>) => {
    setSaved(false);
    setForm(f => ({...f, ...patch}));
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const b = await saveBridge(id, bridgeFormToInput(form));
      setForm(bridgeFormFrom(b));
      setSaved(true);
      if (!id) onNavigate({name: 'bridge', id: b.id});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = () =>
    platformConfirm(
      `Delete ${existing?.name}?`,
      'Its port closes at once and every connection through it ends.',
      'Delete',
      async () => {
        try {
          await deleteBridge(id!);
          onNavigate({name: 'home'});
        } catch (e) {
          setError((e as Error).message);
        }
      },
    );

  const publicOn = !!info?.publicPorts;
  const isPublic = form.from === PUBLIC;
  const health = existing
    ? bridgeHealth(existing, relays, live, publicLive)
    : null;
  const relayName = (rid: string) =>
    relays.find(r => r.id === rid)?.name ?? 'unknown relay';
  const port = form.listenPort || form.targetPort || 'port';
  const domain = domains.find(d => d.id === form.domainId);
  const publicHost = domain
    ? domain.wildcard
      ? `${form.subdomain.trim().toLowerCase() || 'name'}.${domain.name}`
      : domain.name
    : info?.adminHost ?? 'this server';
  // A bridge that has a slug keeps one: clearing the field can't save (the
  // form would leave it out and quietly keep the old one).
  const slugCleared = !!existing?.slug && form.slug.trim() === '';
  const ready =
    form.name.trim() !== '' &&
    !slugCleared &&
    form.from !== '' &&
    form.toRelayId !== '' &&
    form.from !== form.toRelayId &&
    (form.listenPort !== '' || form.targetPort !== '');

  return (
    <ScrollView
      style={styles.fill}
      contentContainerStyle={styles.container}
      keyboardShouldPersistTaps="handled">
      {existing && health && (
        <View style={styles.card}>
          <View style={styles.cardRow}>
            <Dot color={healthColor(theme, health.health)} />
            <Text style={styles.cardTitle} numberOfLines={1}>
              {existing.name}
            </Text>
            <Text style={styles.cardMeta}>
              {existing.protocol.toUpperCase()}
            </Text>
          </View>
          <Text style={styles.cardText}>{health.detail}</Text>
          <Text style={styles.cardText}>{bridgeLine(existing, relayName)}</Text>
          {existing.public && (
            <CopyBox text={existing.publicAddress} label="Public address" />
          )}
        </View>
      )}

      <ManagedNotice managed={existing?.managed} noun="bridge" />
      {!id && strict && (
        <Notice kind="info" title="The schema lists every bridge">
          New bridges go in the schema, so this one would be refused. Add it there, or turn on override in Settings.
        </Notice>
      )}

      <Title>Bridge</Title>
      <Label>Name</Label>
      <Input
        value={form.name}
        onChangeText={v => set({name: v})}
        editable={!lock('name')}
        placeholder="postgres"
        accessibilityLabel="Name"
      />
      <Label spaced>Slug</Label>
      <Input
        value={form.slug}
        onChangeText={v => set({slug: v.toLowerCase()})}
        editable={!pinned}
        placeholder={existing ? '' : 'made from the name'}
        autoCapitalize="none"
        autoCorrect={false}
        accessibilityLabel="Slug"
      />
      <Hint>
        {slugCleared
          ? "A bridge's slug can be changed but not removed."
          : 'The name config files use for this bridge: lowercase letters, digits and dashes.'}
      </Hint>
      <Label spaced>Protocol</Label>
      <Chips<BridgeProtocol>
        options={[
          {value: 'tcp', label: 'TCP'},
          {value: 'udp', label: 'UDP'},
        ]}
        value={form.protocol}
        onChange={v => set({protocol: v})}
      />
      <Hint>
        {form.protocol === 'tcp'
          ? 'Each connection is carried through on its own, so connection pools work as usual.'
          : 'Packets go through as they are. Each sender gets its own socket at the far end, so replies reach the right app.'}
      </Hint>

      <Divider />
      <Title>Client side</Title>
      <Hint>
        Where apps connect. Connections only go one way: from here to the server
        side.
      </Hint>
      <RelayChips
        relays={relays}
        live={live}
        protocol={form.protocol}
        value={form.from}
        exclude={form.toRelayId}
        onChange={v => set({from: v})}
        extra={
          publicOn ? {value: PUBLIC, label: 'Public (internet)'} : undefined
        }
      />
      {!publicOn && (
        <Hint>
          Public bridges (a port on this server, open to the internet) are off.
          Set SUPERMATRIX_PUBLIC_PORTS on the server to allow them.
        </Hint>
      )}
      <Label spaced>Port</Label>
      <Input
        style={styles.port}
        mono
        value={form.listenPort}
        onChangeText={v => set({listenPort: v.replace(/[^0-9]/g, '')})}
        placeholder={form.targetPort || '5432'}
        keyboardType="number-pad"
        accessibilityLabel="Listen port"
      />
      {isPublic ? (
        <>
          <Hint>
            Allowed ports: {info?.publicPorts}. Open them in the server's
            firewall too.
          </Hint>
          <Label spaced>DNS name</Label>
          <View style={styles.chips}>
            {[
              {id: '', label: `None (${info?.adminHost ?? 'admin host'})`},
              ...domains.map(d => ({id: d.id, label: d.label})),
            ].map(o => (
              <Pressable
                key={o.id || 'none'}
                style={[styles.chip, form.domainId === o.id && styles.chipOn]}
                onPress={() => set({domainId: o.id})}
                accessibilityRole="radio"
                accessibilityState={{selected: form.domainId === o.id}}>
                <Text
                  style={[
                    styles.chipText,
                    form.domainId === o.id && styles.chipTextOn,
                  ]}>
                  {o.label}
                </Text>
              </Pressable>
            ))}
          </View>
          {domain?.wildcard && (
            <View style={[styles.subRow, styles.spaced]}>
              <Input
                style={styles.flex}
                value={form.subdomain}
                onChangeText={v => set({subdomain: v})}
                placeholder="turn"
                accessibilityLabel="Name under the domain"
              />
              <Text style={styles.suffix} numberOfLines={1}>
                .{domain.name}
              </Text>
            </View>
          )}
          <Hint>
            People connect to {publicHost}:{port}. The name just has to point at
            this server (a wildcard domain's record already does); the port
            decides which bridge a connection reaches.
          </Hint>
          {existing?.host && existing.host === publicHost && (
            <DnsCheck id={existing.id} />
          )}
          <Notice
            kind="info"
            title="Anyone on the internet can reach this port">
            Raw {form.protocol.toUpperCase()} has no sign-in, so the service's
            own authentication is the only lock. The service sees the relay as
            the client, not the real address.
          </Notice>
          <Label spaced>IP allow list</Label>
          <Input
            value={form.ipAllowlist}
            onChangeText={v => set({ipAllowlist: v})}
            multiline
            mono
            placeholder={'Anyone (empty)\n203.0.113.7\n10.0.0.0/8'}
            style={styles.multiline}
            accessibilityLabel="IP allow list"
          />
          <Hint>
            One address or CIDR range per line. Empty (and no IP groups) lets
            every address in.
          </Hint>
          <GroupPicker kind="ip"
            value={form.ipGroups}
            onChange={v => set({ipGroups: v})}
          />
        </>
      ) : (
        <>
          <Hint>
            On {form.from ? relayName(form.from) : 'that relay'}'s machine, apps
            connect to {form.listenHost.trim() || '127.0.0.1'}:{port}.
          </Hint>
          {advanced ? (
            <>
              <Label spaced>Listen address</Label>
              <Input
                mono
                value={form.listenHost}
                onChangeText={v => set({listenHost: v})}
                placeholder="127.0.0.1"
                accessibilityLabel="Listen address"
              />
              <Hint error={!isLoopback(form.listenHost)}>
                {isLoopback(form.listenHost)
                  ? 'Only programs on that machine can connect.'
                  : 'Anything that can reach this address on that network can use the bridge.'}
              </Hint>
            </>
          ) : (
            <Pressable
              onPress={() => setAdvanced(true)}
              accessibilityRole="button"
              hitSlop={6}>
              <Text style={styles.link}>Listen on another address</Text>
            </Pressable>
          )}
        </>
      )}

      <Divider />
      <Title>Server side</Title>
      <Hint>The relay that reaches the service.</Hint>
      <RelayChips
        relays={relays}
        live={live}
        protocol={form.protocol}
        value={form.toRelayId}
        exclude={isPublic ? '' : form.from}
        onChange={v => set({toRelayId: v})}
      />
      <Label spaced>Service, as that relay sees it</Label>
      <View style={styles.subRow}>
        <Input
          style={styles.flex}
          mono
          value={form.targetHost}
          onChangeText={v => set({targetHost: v})}
          placeholder="127.0.0.1"
          accessibilityLabel="Target host"
        />
        <Text style={styles.colon}>:</Text>
        <Input
          style={styles.port}
          mono
          value={form.targetPort}
          onChangeText={v => set({targetPort: v.replace(/[^0-9]/g, '')})}
          placeholder={form.listenPort || '5432'}
          keyboardType="number-pad"
          accessibilityLabel="Target port"
        />
      </View>
      <Hint>
        127.0.0.1 for a service on that relay's own machine, or another host on
        its network.
      </Hint>

      {form.protocol === 'udp' && (
        <>
          <Label spaced>Forget a quiet sender after</Label>
          <Chips<number>
            options={
              IDLE_OPTIONS.some(o => o.value === form.udpIdleS)
                ? IDLE_OPTIONS
                : [
                    ...IDLE_OPTIONS,
                    {value: form.udpIdleS, label: `${form.udpIdleS} s`},
                  ]
            }
            value={form.udpIdleS}
            onChange={v => set({udpIdleS: v})}
          />
          <Hint>
            Active senders are never cut off. A sender quiet for longer gets a
            new source port at the far end when it speaks again. For TURN
            servers, use 15 minutes or more.
          </Hint>
        </>
      )}

      <Divider />
      <ToggleRow
        label="On"
        hint="Off closes the port; the settings stay for when it's turned back on."
        value={form.enabled}
        onChange={v => set({enabled: v})}
      />

      {error ? (
        <Notice kind="error" title="Not saved">
          {error}
        </Notice>
      ) : null}
      {saved && !error ? <Notice kind="success" title="Saved" /> : null}

      <Button
        label={id ? 'Save' : 'Create bridge'}
        onPress={save}
        busy={busy}
        disabled={!ready}
        style={styles.save}
      />

      {id && !pinned && (
        <>
          <Divider />
          <Button label="Delete bridge" kind="danger" onPress={remove} />
        </>
      )}
    </ScrollView>
  );
}

/**
 * Where a bridge goes: "client laptop 127.0.0.1:5432 → server homelab
 * 127.0.0.1:5432", or for a public one "turn.example.com:3478 → server …".
 */
export function bridgeLine(
  b: Bridge,
  relayName: (id: string) => string,
): string {
  const client = b.public
    ? b.publicAddress
    : `client ${relayName(b.fromRelayId)} ${b.listenHost || '127.0.0.1'}:${
        b.listenPort
      }`;
  return `${client} → server ${relayName(b.toRelayId)} ${b.targetHost}:${
    b.targetPort
  }`;
}

/** Relays to pick from; ones that can't serve this kind of bridge are greyed out. */
function RelayChips({
  relays,
  live,
  protocol,
  value,
  exclude,
  onChange,
  extra,
}: {
  relays: Relay[];
  live: Record<string, RelayState>;
  protocol: BridgeProtocol;
  value: string;
  exclude: string;
  onChange: (id: string) => void;
  extra?: {value: string; label: string};
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  if (relays.length === 0 && !extra)
    return <Hint error>Add a relay first (Relays, at the top).</Hint>;
  return (
    <View style={styles.chips}>
      {extra && (
        <Pressable
          style={[styles.chip, value === extra.value && styles.chipOn]}
          onPress={() => onChange(extra.value)}
          accessibilityRole="radio"
          accessibilityState={{selected: value === extra.value}}>
          <Text
            style={[
              styles.chipText,
              value === extra.value && styles.chipTextOn,
            ]}>
            {extra.label}
          </Text>
        </Pressable>
      )}
      {relays
        .filter(r => r.id !== exclude)
        .map(r => {
          const ok = supportsBridges(r, protocol);
          const on = value === r.id;
          return (
            <Pressable
              key={r.id}
              style={[styles.chip, on && styles.chipOn, !ok && styles.chipOff]}
              onPress={() => ok && onChange(r.id)}
              disabled={!ok}
              accessibilityRole="radio"
              accessibilityState={{selected: on, disabled: !ok}}>
              <Dot color={live[r.id] ? theme.success : theme.textMuted} />
              <Text style={[styles.chipText, on && styles.chipTextOn]}>
                {r.name}
              </Text>
              <Text style={[styles.chipMeta, on && styles.chipMetaOn]}>
                {!ok
                  ? 'needs relay update'
                  : live[r.id]
                  ? transportBadge(live[r.id])
                  : 'offline'}
              </Text>
            </Pressable>
          );
        })}
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {
      padding: 20,
      paddingBottom: 48,
      maxWidth: 560,
      width: '100%',
      alignSelf: 'center',
    },
    card: {
      padding: 14,
      borderRadius: 10,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.surface,
      marginBottom: 20,
      gap: 4,
    },
    cardRow: {flexDirection: 'row', alignItems: 'center', gap: 8},
    cardTitle: {flex: 1, fontSize: 15, fontWeight: '700', color: theme.text},
    cardMeta: {fontSize: 11, fontWeight: '700', color: theme.textMuted},
    cardText: {fontSize: 12, color: theme.textMuted, lineHeight: 17},
    subRow: {flexDirection: 'row', alignItems: 'center', gap: 6},
    flex: {flex: 1, minWidth: 0},
    colon: {fontSize: 16, color: theme.textMuted},
    port: {width: 90},
    link: {fontSize: 13, color: theme.primary, fontWeight: '600', marginTop: 8},
    multiline: {minHeight: 76, textAlignVertical: 'top'},
    save: {marginTop: 20},
    chips: {flexDirection: 'row', flexWrap: 'wrap', gap: 8},
    chip: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 6,
      paddingHorizontal: 12,
      paddingVertical: 8,
      borderRadius: 6,
      backgroundColor: theme.surfaceAlt,
    },
    chipOn: {backgroundColor: theme.primary},
    chipOff: {opacity: 0.45},
    chipText: {fontWeight: '600', fontSize: 13, color: theme.text},
    chipTextOn: {color: theme.primaryText},
    chipMeta: {fontSize: 11, color: theme.textMuted},
    chipMetaOn: {color: theme.primaryText, opacity: 0.85},
    spaced: {marginTop: 10},
    suffix: {
      fontSize: 14,
      color: theme.textMuted,
      fontFamily: 'monospace',
      flexShrink: 1,
    },
  });
}

/** Checks that a public bridge's DNS name points at this server. */
function DnsCheck({id}: {id: string}) {
  const [busy, setBusy] = useState(false);
  const [res, setRes] = useState<DomainDns | null>(null);
  const [error, setError] = useState<string | null>(null);
  const check = async () => {
    setBusy(true);
    setError(null);
    try {
      setRes(await checkBridgeDns(id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <View>
      <Button label="Check DNS" kind="secondary" onPress={check} busy={busy} />
      {res && <Hint error={!res.ok}>{res.detail}</Hint>}
      {error && <Hint error>{error}</Hint>}
    </View>
  );
}
