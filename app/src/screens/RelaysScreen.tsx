import React, {useState} from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {Relay, RelayState} from '../api';
import {Icon} from '../components/Icon';
import {isFallback, linkBadge, linkDetails, quicHint} from '../link';
import {Notice} from '../components/Notice';
import {Overlay} from '../components/Overlay';
import {Button, Chips, CopyBox, Divider, Dot, Hint, Input, Label, Title} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {createRelay, deleteRelay, renameRelay, rotateRelay, useStore} from '../store';
import {Theme, useTheme} from '../theme';

export const REPO = 'github:canavan-a/super-matrix';

type InstallKind = 'nixos' | 'systemd' | 'run';

/** How to run a relay with this token, for each way of installing it. */
export function relayInstall(serverUrl: string, token: string): Record<InstallKind, string[]> {
  return {
    nixos: [
      `# flake inputs\nsuper-matrix.url = "${REPO}";`,
      `# configuration\nimports = [ inputs.super-matrix.nixosModules.relay ];\nservices.supermatrix-relay = {\n  enable = true;\n  serverUrl = "${serverUrl}";\n  tokenFile = "/etc/supermatrix/relay-token";\n};`,
      `# the token file (or use your secrets manager, e.g. agenix/sops)\nsudo mkdir -p /etc/supermatrix\necho '${token}' | sudo install -m 600 /dev/stdin /etc/supermatrix/relay-token`,
    ],
    systemd: [
      `git clone https://github.com/canavan-a/super-matrix\ncd super-matrix\nsudo deploy/install.sh relay '${token}'`,
      `sudo sed -i 's|^SUPERMATRIX_SERVER_URL=.*|SUPERMATRIX_SERVER_URL=${serverUrl}|' /etc/supermatrix/relay.env\nsudo systemctl enable --now supermatrix-relay`,
    ],
    run: [
      `cd super-matrix/server\nSUPERMATRIX_SERVER_URL=${serverUrl} \\\nSUPERMATRIX_RELAY_TOKEN='${token}' \\\ngo run ./cmd/supermatrix-relay`,
    ],
  };
}

const INSTALL_OPTIONS: {value: InstallKind; label: string}[] = [
  {value: 'nixos', label: 'NixOS'},
  {value: 'systemd', label: 'systemd'},
  {value: 'run', label: 'Just run it'},
];

function ago(iso: string | null): string {
  if (!iso) return 'never connected';
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 90) return 'seen just now';
  if (s < 3600 * 1.5) return `seen ${Math.round(s / 60)} min ago`;
  if (s < 86400 * 1.5) return `seen ${Math.round(s / 3600)} h ago`;
  return `seen ${Math.round(s / 86400)} days ago`;
}

/** The relays on this server: add one (token + install steps), rename, rotate, delete. */
export function RelaysScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const relays = useStore(s => s.relays);
  const live = useStore(s => s.live);
  const serverUrl = useStore(s => s.info?.serverUrl ?? s.server ?? '');
  const info = useStore(s => s.info);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [sheet, setSheet] = useState<{
    name: string;
    token: string;
    rotated: boolean;
  } | null>(null);
  const [open, setOpen] = useState<string | null>(null);

  const add = async () => {
    setBusy(true);
    setError(null);
    try {
      const {relay, token} = await createRelay(name.trim());
      setName('');
      setSheet({name: relay.name, token, rotated: false});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <View style={styles.fill}>
      <ScrollView contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
        <Title>Add a relay</Title>
        <Hint>
          A relay runs next to the services you expose (a home server, a Pi, a laptop). It dials out to this server, so the machine needs no
          open ports or public address.
        </Hint>
        <View style={[styles.row, styles.spaced]}>
          <Input style={styles.flex} value={name} onChangeText={setName} placeholder="Name, e.g. homelab" onSubmitEditing={add} />
          <Button label="Add" onPress={add} busy={busy} disabled={!name.trim()} />
        </View>
        {error ? (
          <Notice kind="error" title="Couldn't do that">
            {error}
          </Notice>
        ) : null}

        <Divider />
        <Title>Relays</Title>
        {info ? <Hint>{quicHint(info)}</Hint> : null}
        {relays.length === 0 && <Hint>None yet.</Hint>}
        {relays.map(r => (
          <RelayRow
            key={r.id}
            r={r}
            state={live[r.id]}
            fallback={isFallback(info, live[r.id])}
            hostname={live[r.id]?.hostname || r.hostname}
            version={live[r.id]?.version || r.version}
            open={open === r.id}
            onToggle={() => setOpen(o => (o === r.id ? null : r.id))}
            onRotated={token => setSheet({name: r.name, token, rotated: true})}
            onError={setError}
          />
        ))}
      </ScrollView>

      <Overlay visible={!!sheet} onClose={() => setSheet(null)} modal style={styles.sheetWrap}>
        {sheet && <TokenSheet {...sheet} serverUrl={serverUrl} onClose={() => setSheet(null)} />}
      </Overlay>
    </View>
  );
}

function RelayRow({
  r,
  state,
  fallback,
  hostname,
  version,
  open,
  onToggle,
  onRotated,
  onError,
}: {
  r: Relay;
  state: RelayState | undefined;
  /** on the WebSocket although the server offers QUIC */
  fallback: boolean;
  hostname: string;
  version: string;
  open: boolean;
  onToggle: () => void;
  onRotated: (token: string) => void;
  onError: (msg: string) => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [newName, setNewName] = useState(r.name);
  const fail = (e: unknown) => onError((e as Error).message);
  const online = !!state;
  const badge = linkBadge(r, state);

  return (
    <View style={styles.relay}>
      <Pressable style={styles.relayHead} onPress={onToggle} accessibilityRole="button" accessibilityState={{expanded: open}}>
        <Dot color={online ? theme.success : theme.textMuted} />
        <View style={styles.flex}>
          <Text style={styles.relayName}>{r.name}</Text>
          <Text style={styles.relayMeta} numberOfLines={1}>
            {online ? 'online' : ago(r.lastSeenAt)}
            {hostname ? ` · ${hostname}` : ''}
            {version ? ` · v${version}` : ''} · {r.deployments} deployment
            {r.deployments === 1 ? '' : 's'}
          </Text>
          {badge ? (
            <Text style={[styles.relayMeta, fallback && styles.fallback]} numberOfLines={1}>
              {badge}
            </Text>
          ) : null}
        </View>
        <Icon name={open ? 'chevron-down' : 'chevron-right'} size={18} color={theme.textMuted} />
      </Pressable>
      {open && (
        <View style={styles.relayBody}>
          {r.lastIp && !online ? <Hint>Last address: {r.lastIp}</Hint> : null}
          {linkDetails(r, state).map(line => (
            <Hint key={line}>{line}</Hint>
          ))}
          <Label spaced>Name</Label>
          <View style={styles.row}>
            <Input style={styles.flex} value={newName} onChangeText={setNewName} />
            <Button
              label="Rename"
              kind="secondary"
              disabled={!newName.trim() || newName.trim() === r.name}
              onPress={() => renameRelay(r.id, newName.trim()).catch(fail)}
            />
          </View>
          <View style={[styles.row, styles.spaced]}>
            <Button
              label="New token"
              kind="secondary"
              icon="retry"
              onPress={() =>
                platformConfirm(
                  `New token for ${r.name}?`,
                  'The old token stops working at once and the relay disconnects until you give it the new one.',
                  'Replace',
                  () => rotateRelay(r.id).then(onRotated).catch(fail),
                  false,
                )
              }
            />
            <Button
              label="Delete"
              kind="secondary"
              icon="trash"
              onPress={() =>
                platformConfirm(`Delete ${r.name}?`, 'Its token stops working. Move or delete its deployments first.', 'Delete', () =>
                  deleteRelay(r.id).catch(fail),
                )
              }
            />
          </View>
        </View>
      )}
    </View>
  );
}

function TokenSheet({
  name,
  token,
  rotated,
  serverUrl,
  onClose,
}: {
  name: string;
  token: string;
  rotated: boolean;
  serverUrl: string;
  onClose: () => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [kind, setKind] = useState<InstallKind>('nixos');
  return (
    <View style={styles.sheet}>
      <ScrollView contentContainerStyle={styles.sheetContent}>
        <View style={styles.sheetHead}>
          <Text style={styles.sheetTitle}>{rotated ? `New token for ${name}` : `${name} is ready`}</Text>
          <Pressable onPress={onClose} accessibilityLabel="Close" hitSlop={8}>
            <Icon name="close" size={20} color={theme.text} />
          </Pressable>
        </View>
        <Hint>This token is shown once. It lets a relay connect as {name}; keep it like a password.</Hint>
        <CopyBox text={token} label="relay token" />

        <Label spaced>Install it</Label>
        <Chips options={INSTALL_OPTIONS} value={kind} onChange={setKind} />
        {relayInstall(serverUrl, token)[kind].map((snippet, i) => (
          <CopyBox key={`${kind}${i}`} text={snippet} />
        ))}
        <Hint>It shows up as online here once it connects.</Hint>
        <Button label="Done" onPress={onClose} style={styles.spaced} />
      </ScrollView>
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
    row: {flexDirection: 'row', alignItems: 'center', gap: 8},
    flex: {flex: 1, minWidth: 0},
    spaced: {marginTop: 12},
    relay: {
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 10,
      backgroundColor: theme.surface,
      marginBottom: 8,
    },
    relayHead: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      padding: 14,
    },
    relayName: {fontSize: 15, fontWeight: '700', color: theme.text},
    relayMeta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
    fallback: {color: theme.danger},
    relayBody: {
      paddingHorizontal: 14,
      paddingBottom: 14,
      borderTopWidth: 1,
      borderTopColor: theme.border,
    },
    sheetWrap: {justifyContent: 'center', alignItems: 'center', padding: 16},
    sheet: {
      width: '100%',
      maxWidth: 560,
      maxHeight: '92%',
      borderRadius: 12,
      backgroundColor: theme.bg,
      borderWidth: 1,
      borderColor: theme.border,
    },
    sheetContent: {padding: 20},
    sheetHead: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: 10,
    },
    sheetTitle: {fontSize: 17, fontWeight: '700', color: theme.text, flex: 1},
  });
}
