import React, {useState} from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import type {NixService, Relay} from '../api';
import {Icon} from '../components/Icon';
import {ManagedBadge, useStrict} from '../components/Managed';
import {Button, Chips, CopyBox, Divider, Hint, Label, Title} from '../components/ui';
import {PhaseBadge, progressLine} from '../nix/phase';
import type {Route} from '../routes';
import {useStore} from '../store';
import {Theme, useTheme} from '../theme';

const RUN: Record<NixService['run'], string> = {package: 'package', app: 'app', devshell: 'dev shell'};

type Setup = 'nixos' | 'other';
const SETUP_OPTIONS: {value: Setup; label: string}[] = [
  {value: 'nixos', label: 'NixOS'},
  {value: 'other', label: 'Other Linux'},
];
/** How a relay's machine gets the Nix agent, which builds and runs them. */
const SETUP: Record<Setup, string> = {
  nixos: `# configuration (the relay module, as set up in Relays)\nservices.supermatrix-relay = {\n  enable = true;\n  # …serverUrl, tokenFile as before\n  nixServices.enable = true;  # the Nix agent (runs as root)\n};`,
  other: `# needs Nix (with flakes) and systemd; in the clone the relay was installed from\ncd super-matrix\nsudo deploy/install.sh relay --nix-services\nsudo systemctl restart supermatrix-relay`,
};

/**
 * Nix services: flakes the server keeps and relays build and run. Each
 * card shows where it runs and what it's doing there.
 */
export function NixServicesScreen({onNavigate}: {onNavigate: (r: Route) => void}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const services = useStore(s => s.nixServices);
  const relays = useStore(s => s.relays);
  const strict = useStrict('nix_services');
  const ready = relays.filter(r => r.nix?.ready);
  const [setup, setSetup] = useState<Setup>('nixos');

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container}>
      <Title>Nix services</Title>
      <Hint>
        A Nix service is a flake (flake.nix, flake.lock and any other files) and how to run it. Apply it to relays and each relay's
        machine builds it and runs it as a systemd service, smx-&lt;slug&gt;, coming back after reboots and torn down when you remove it.
      </Hint>
      <Label spaced>Letting a relay run them</Label>
      <Chips options={SETUP_OPTIONS} value={setup} onChange={setSetup} />
      <CopyBox text={SETUP[setup]} />
      <Hint>
        Then, in Relays, turn on Nix services for that relay. Its machine runs the Nix agent, and the toggle has to be on too: both
        are needed before anything runs there. Turn on Manage its Nix services as well to let root on that machine define and run them
        itself with supermatrix-relay nix.
      </Hint>
      <ReadyHint relays={relays} ready={ready} />
      {strict ? (
        <Hint>The schema lists every Nix service: add new ones there (or turn on override in Settings).</Hint>
      ) : (
        <Button label="New Nix service" icon="plus" onPress={() => onNavigate({name: 'nixService', id: null})} style={styles.spaced} />
      )}

      <Divider />
      {services.length === 0 && <Hint>None yet.</Hint>}
      {services.map(s => (
        <Pressable
          key={s.id}
          style={styles.card}
          onPress={() => onNavigate({name: 'nixService', id: s.id})}
          accessibilityRole="button"
          accessibilityLabel={s.name}>
          <View style={styles.head}>
            <View style={styles.flex}>
              <View style={styles.nameRow}>
                <Text style={styles.name}>{s.name}</Text>
                <ManagedBadge managed={s.managed} />
              </View>
              <Text style={styles.meta}>
                {s.slug} · {RUN[s.run]}
                {s.attr !== 'default' ? ` ${s.attr}` : ''} · {Object.keys(s.files).length} file
                {Object.keys(s.files).length === 1 ? '' : 's'}
                {s.flakeLock ? (s.lockPinned ? ' · lock pinned' : ' · locked') : ' · not locked yet'}
                {s.pendingLock ? ' · a lock waits for you' : ''}
              </Text>
            </View>
            <Icon name="chevron-right" size={18} color={theme.textMuted} />
          </View>
          {s.applied.length === 0 ? (
            <Text style={styles.none}>Not applied to any relay</Text>
          ) : (
            s.applied.map(a => (
              <Pressable
                key={a.id}
                style={styles.applied}
                onPress={() => onNavigate({name: 'appliedNix', id: a.id})}
                accessibilityRole="button"
                accessibilityLabel={`${s.name} on ${a.relayName}`}>
                <Text style={styles.relay} numberOfLines={1}>
                  {a.relayName}
                </Text>
                <PhaseBadge a={a} />
                {a.phase === 'building' && a.progress ? (
                  <Text style={styles.progress} numberOfLines={1}>
                    {progressLine(a.progress)}
                  </Text>
                ) : !a.upToDate && a.serving && a.phase === 'running' ? (
                  <Text style={styles.progress}>older build</Text>
                ) : null}
              </Pressable>
            ))
          )}
        </Pressable>
      ))}
    </ScrollView>
  );
}

/** Which relays can run them, and if none can, why. */
function ReadyHint({relays, ready}: {relays: Relay[]; ready: Relay[]}) {
  if (relays.length === 0) return null;
  if (ready.length > 0)
    return (
      <Hint>
        Ready to run them: {ready.map(r => r.name).join(', ')}. Other relays show what they need in Relays.
      </Hint>
    );
  const first = relays.find(r => r.nix)?.nix;
  return (
    <Hint error>
      No relay can run Nix services yet.{' '}
      {first ? first.reason + '.' : 'Update this server and its relays first.'} Each relay shows what it needs in Relays.
    </Hint>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 560, width: '100%', alignSelf: 'center'},
    spaced: {marginTop: 12},
    flex: {flex: 1, minWidth: 0},
    card: {borderWidth: 1, borderColor: theme.border, borderRadius: 10, backgroundColor: theme.surface, marginBottom: 8, padding: 14},
    head: {flexDirection: 'row', alignItems: 'center', gap: 10},
    nameRow: {flexDirection: 'row', alignItems: 'center', gap: 6},
    name: {fontSize: 15, fontWeight: '700', color: theme.text},
    meta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
    none: {fontSize: 12, color: theme.textMuted, marginTop: 10},
    applied: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      marginTop: 10,
      paddingTop: 10,
      borderTopWidth: 1,
      borderTopColor: theme.border,
    },
    relay: {fontSize: 13, fontWeight: '600', color: theme.text, minWidth: 80},
    progress: {fontSize: 12, color: theme.textMuted, flex: 1, minWidth: 0},
  });
}
