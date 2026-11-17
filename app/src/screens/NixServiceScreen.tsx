import React, {useMemo, useState} from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import type {NixRestart, NixRun, NixServiceInput} from '../api';
import {Icon} from '../components/Icon';
import {ManagedNotice, useLock, useResourceLocked} from '../components/Managed';
import {Notice} from '../components/Notice';
import {Button, Chips, Divider, Hint, Input, Label, Title, ToggleRow} from '../components/ui';
import {PhaseBadge, progressLine} from '../nix/phase';
import {canPickFolder, pickFolder} from '../nix/pickFolder';
import {platformConfirm} from '../platformConfirm';
import type {Route} from '../routes';
import {applyNixService, decideNixLock, deleteNixService, relockNixService, saveNixService, useStore} from '../store';
import {Theme, useTheme} from '../theme';

const STARTER = `{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
  outputs = { nixpkgs, ... }:
    let pkgs = nixpkgs.legacyPackages.x86_64-linux;
    in {
      packages.x86_64-linux.default = pkgs.uptime-kuma;
    };
}
`;

/** KEY=value lines → a map (blank lines and # comments skipped). */
export function parseEnv(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split('\n')) {
    const l = line.trim();
    if (!l || l.startsWith('#')) continue;
    const i = l.indexOf('=');
    if (i <= 0) out[l] = '';
    else out[l.slice(0, i).trim()] = l.slice(i + 1);
  }
  return out;
}

export const envText = (env: Record<string, string>): string =>
  Object.keys(env)
    .sort()
    .map(k => `${k}=${env[k]}`)
    .join('\n');

/** One per line (commas work too for names), blanks dropped. */
const lines = (text: string, commas = false): string[] =>
  text
    .split(commas ? /[\n,]/ : /\n/)
    .map(s => s.trim())
    .filter(Boolean);

const RUN_OPTIONS: {value: NixRun; label: string}[] = [
  {value: 'package', label: 'Package'},
  {value: 'app', label: 'App'},
  {value: 'devshell', label: 'Dev shell'},
];

const RESTART_OPTIONS: {value: NixRestart; label: string}[] = [
  {value: 'always', label: 'Always'},
  {value: 'on-failure', label: 'On failure'},
  {value: 'no', label: 'Never'},
];

const RUN_HINT: Record<NixRun, string> = {
  package: "Runs packages.<system>.<output>'s main program (meta.mainProgram), or the command below.",
  app: "Runs apps.<system>.<output>'s program, or the command below.",
  devshell: "Runs the command below inside devShells.<system>.<output>'s environment, from the flake's directory.",
};

/** A Nix service: its flake and how it runs (id null: a new one), and where it's applied. */
export function NixServiceScreen({id, onNavigate}: {id: string | null; onNavigate: (r: Route) => void}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const service = useStore(s => s.nixServices.find(x => x.id === id) ?? null);
  const relays = useStore(s => s.relays);
  const pinned = useResourceLocked(service?.managed);
  const lock = useLock(service?.managed);

  const [name, setName] = useState(service?.name ?? '');
  const [slug, setSlug] = useState(service?.slug ?? '');
  const [run, setRun] = useState<NixRun>(service?.run ?? 'package');
  const [attr, setAttr] = useState(service?.attr ?? 'default');
  const [command, setCommand] = useState((service?.command ?? []).join('\n'));
  const [env, setEnv] = useState(envText(service?.env ?? {}));
  const [relayEnv, setRelayEnv] = useState((service?.relayEnv ?? []).join('\n'));
  const [restart, setRestart] = useState<NixRestart>(service?.restart ?? 'always');
  const [removeState, setRemoveState] = useState(service?.removeState ?? true);
  const [files, setFiles] = useState<Record<string, string>>(service?.files ?? {'flake.nix': STARTER});
  const [lockText, setLockText] = useState(service?.flakeLock ?? '');
  const [lockEdited, setLockEdited] = useState(false);
  const [editLock, setEditLock] = useState(false);
  const [tab, setTab] = useState('flake.nix');
  const [newFile, setNewFile] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const fileNames = useMemo(() => ['flake.nix', ...Object.keys(files).filter(f => f !== 'flake.nix').sort()], [files]);
  const filesLocked = lock('flake') || lock('files');

  const fail = (e: unknown) => setError((e as Error).message);

  const save = async () => {
    setBusy(true);
    setError(null);
    setSaved(false);
    try {
      const input: NixServiceInput = {
        name: name.trim(),
        run,
        attr: attr.trim() || 'default',
        command: lines(command),
        env: parseEnv(env),
        relayEnv: lines(relayEnv, true),
        restart,
        removeState,
        files,
      };
      if (slug.trim() && slug.trim() !== service?.slug) input.slug = slug.trim();
      // Only a lock typed (or uploaded) here is sent: sending one pins it.
      if (lockEdited) input.flakeLock = lockText;
      const out = await saveNixService(service?.id ?? null, input);
      setLockEdited(false);
      setEditLock(false);
      setSaved(true);
      if (!service) onNavigate({name: 'nixService', id: out.id});
    } catch (e) {
      fail(e);
    } finally {
      setBusy(false);
    }
  };

  const upload = async () => {
    try {
      const picked = await pickFolder();
      if (!picked) return;
      if (!picked['flake.nix']) throw new Error('That folder has no flake.nix');
      const {['flake.lock']: lockFile, ...rest} = picked;
      setFiles(rest);
      if (lockFile !== undefined) {
        setLockText(lockFile);
        setLockEdited(true);
      }
      setTab('flake.nix');
    } catch (e) {
      fail(e);
    }
  };

  const applied = service?.applied ?? [];
  const appliedTo = new Set(applied.filter(a => !a.removing).map(a => a.relayId));
  const candidates = relays.filter(r => !appliedTo.has(r.id));
  const relaysLocked = lock('relays');

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      <ManagedNotice managed={service?.managed} noun="Nix service" />
      {service?.pendingLock ? <PendingLock service={service} onError={fail} /> : null}
      <Title>{service ? service.name : 'New Nix service'}</Title>

      <Label>Name</Label>
      <Input value={name} onChangeText={setName} editable={!lock('name')} placeholder="Uptime Kuma" accessibilityLabel="Name" />
      <Label spaced>Slug</Label>
      <Input
        value={slug}
        onChangeText={v => setSlug(v.toLowerCase())}
        editable={!pinned}
        placeholder={service ? '' : 'made from the name'}
        accessibilityLabel="Slug"
      />
      <Hint>Names its unit (smx-{slug || 'slug'}.service), its state directory and its env file on each relay.</Hint>

      <Label spaced>How it runs</Label>
      <Chips options={RUN_OPTIONS} value={run} onChange={setRun} disabled={lock('run')} />
      <Hint>{RUN_HINT[run]}</Hint>
      <Label spaced>Output</Label>
      <Input value={attr} onChangeText={setAttr} editable={!lock('attr')} placeholder="default" accessibilityLabel="Output" />
      <Label spaced>Command{run === 'devshell' ? '' : ' (optional)'}</Label>
      <Input
        value={command}
        onChangeText={setCommand}
        editable={!lock('command')}
        multiline
        mono
        style={styles.multiline}
        placeholder={run === 'devshell' ? 'npm\nstart' : 'uptime-kuma-server\n--port\n3001'}
        accessibilityLabel="Command"
      />
      <Hint>The program, then one argument per line. A name without a slash is in the output's bin/.</Hint>

      <Label spaced>Environment</Label>
      <Input
        value={env}
        onChangeText={setEnv}
        editable={!lock('env')}
        multiline
        mono
        style={styles.multiline}
        placeholder={'PORT=3001'}
        accessibilityLabel="Environment"
      />
      <Hint>KEY=value, one per line. Relays can override them, each its own way.</Hint>
      <Label spaced>Set on each relay</Label>
      <Input
        value={relayEnv}
        onChangeText={setRelayEnv}
        editable={!lock('relay_env')}
        mono
        placeholder="DB_PASSWORD, SMTP_PASS"
        accessibilityLabel="Relay env"
      />
      <Hint>
        Secrets stay on the machines: each relay sets these in /etc/supermatrix/nix-services/{slug || '<slug>'}.env, and the service
        waits until they're there. The server never sees them.
      </Hint>

      <Label spaced>Restart</Label>
      <Chips options={RESTART_OPTIONS} value={restart} onChange={setRestart} disabled={lock('restart')} />
      <View style={styles.spaced}>
        <ToggleRow
          label="Delete its data when removed"
          hint={`Removing it from a relay also deletes /var/lib/smx/${slug || '<slug>'}.`}
          value={removeState}
          onChange={setRemoveState}
          disabled={lock('teardown')}
        />
      </View>

      <Divider />
      <Title>Files</Title>
      <Hint>
        Kept on the server and written next to each other on the relays. In a schema, source: ./dir reads a folder for you.
      </Hint>
      <View style={styles.tabs}>
        {[...fileNames, 'flake.lock'].map(f => (
          <Pressable
            key={f}
            style={[styles.tab, tab === f && styles.tabOn]}
            onPress={() => setTab(f)}
            accessibilityRole="tab"
            accessibilityState={{selected: tab === f}}>
            <Text style={[styles.tabText, tab === f && styles.tabTextOn]}>{f}</Text>
          </Pressable>
        ))}
      </View>
      {tab === 'flake.lock' ? (
        <>
          <Hint>
            {lockText
              ? service?.lockPinned || lockEdited
                ? 'Pinned: every relay builds with exactly this lock.'
                : 'Made by the first relay to build it; every relay uses it.'
              : 'No lock yet: the first relay to build it makes one, and the rest use it.'}
          </Hint>
          <Input
            value={lockText}
            onChangeText={v => {
              setLockText(v);
              setLockEdited(true);
            }}
            editable={editLock && !lock('flake_lock')}
            multiline
            mono
            style={styles.editor}
            placeholder="(none yet)"
            accessibilityLabel="flake.lock"
          />
          <View style={[styles.row, styles.spaced]}>
            {!editLock && !lock('flake_lock') && <Button label="Pin a lock" kind="secondary" onPress={() => setEditLock(true)} />}
            {service && !lock('flake_lock') && (
              <Button
                label="Update inputs"
                kind="secondary"
                icon="retry"
                onPress={() =>
                  platformConfirm(
                    'Update every input?',
                    "This drops the lock: the next relay to build locks each input to its newest version, and every relay it's on rebuilds.",
                    'Update',
                    () =>
                      relockNixService(service.id)
                        .then(() => {
                          setLockText('');
                          setLockEdited(false);
                        })
                        .catch(fail),
                    false,
                  )
                }
              />
            )}
          </View>
        </>
      ) : (
        <>
          <Input
            value={files[tab] ?? ''}
            onChangeText={v => setFiles(f => ({...f, [tab]: v}))}
            editable={!filesLocked}
            multiline
            mono
            style={styles.editor}
            accessibilityLabel={tab}
          />
          {tab !== 'flake.nix' && !filesLocked && (
            <Button
              label={`Remove ${tab}`}
              kind="secondary"
              icon="trash"
              style={styles.spaced}
              onPress={() => {
                setFiles(({[tab]: _, ...rest}) => rest);
                setTab('flake.nix');
              }}
            />
          )}
        </>
      )}
      {!filesLocked && (
        <View style={[styles.row, styles.spaced]}>
          <Input style={styles.flex} value={newFile} onChangeText={setNewFile} placeholder="config/app.json" accessibilityLabel="New file" />
          <Button
            label="Add file"
            kind="secondary"
            disabled={!newFile.trim() || newFile.trim() in files || newFile.trim() === 'flake.lock'}
            onPress={() => {
              const f = newFile.trim().replace(/^\.\//, '');
              setFiles(x => ({...x, [f]: ''}));
              setTab(f);
              setNewFile('');
            }}
          />
          {canPickFolder && <Button label="Upload folder" kind="secondary" onPress={upload} />}
        </View>
      )}

      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}
      {saved && !error ? <Notice kind="success" title={applied.length ? 'Saved: the relays it runs on pick it up now' : 'Saved'} /> : null}
      <Button label={service ? 'Save' : 'Create'} onPress={save} busy={busy} disabled={!name.trim() || !files['flake.nix']?.trim()} style={styles.spaced} />

      {service && (
        <>
          <Divider />
          <Title>Where it runs</Title>
          {applied.length === 0 && <Hint>Not on any relay yet.</Hint>}
          {applied.map(a => (
            <Pressable
              key={a.id}
              style={styles.applied}
              onPress={() => onNavigate({name: 'appliedNix', id: a.id})}
              accessibilityRole="button"
              accessibilityLabel={`On ${a.relayName}`}>
              <View style={styles.flex}>
                <Text style={styles.relay}>{a.relayName}</Text>
                {a.phase === 'building' && a.progress ? <Text style={styles.meta}>{progressLine(a.progress)}</Text> : null}
              </View>
              <PhaseBadge a={a} />
              <Icon name="chevron-right" size={18} color={theme.textMuted} />
            </Pressable>
          ))}
          {relaysLocked ? (
            <Hint>The schema says which relays it runs on.</Hint>
          ) : candidates.length > 0 ? (
            <>
              <Label spaced>Run it on</Label>
              {candidates.map(r => (
                <View key={r.id} style={styles.candidate}>
                  <View style={styles.flex}>
                    <Text style={styles.relay}>{r.name}</Text>
                    {!r.nix?.ready ? (
                      <Text style={styles.meta}>{r.nix?.reason ?? 'This server is too old to say'}</Text>
                    ) : null}
                  </View>
                  <Button
                    label="Apply"
                    kind="secondary"
                    disabled={!r.nix?.ready}
                    onPress={() =>
                      applyNixService(service.id, r.id)
                        .then(a => onNavigate({name: 'appliedNix', id: a.id}))
                        .catch(fail)
                    }
                  />
                </View>
              ))}
            </>
          ) : null}

          {!pinned && (
            <Button
              label="Delete"
              kind="danger"
              icon="trash"
              style={styles.delete}
              onPress={() =>
                platformConfirm(
                  `Delete ${service.name}?`,
                  applied.length
                    ? `It's stopped and torn down on ${applied.length} relay${applied.length === 1 ? '' : 's'}${service.removeState ? ', its data with it' : ''}.`
                    : 'It runs nowhere.',
                  'Delete',
                  () =>
                    deleteNixService(service.id)
                      .then(() => onNavigate({name: 'nix'}))
                      .catch(fail),
                )
              }
            />
          )}
        </>
      )}
    </ScrollView>
  );
}


function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 720, width: '100%', alignSelf: 'center'},
    row: {flexDirection: 'row', alignItems: 'center', gap: 8, flexWrap: 'wrap'},
    flex: {flex: 1, minWidth: 0},
    spaced: {marginTop: 12},
    multiline: {minHeight: 72, textAlignVertical: 'top'},
    editor: {minHeight: 260, textAlignVertical: 'top', fontSize: 13, lineHeight: 19},
    tabs: {flexDirection: 'row', flexWrap: 'wrap', gap: 6, marginTop: 12, marginBottom: 8},
    tab: {paddingHorizontal: 10, paddingVertical: 6, borderRadius: 6, backgroundColor: theme.surfaceAlt},
    tabOn: {backgroundColor: theme.primary},
    tabText: {fontFamily: 'monospace', fontSize: 12, color: theme.text},
    tabTextOn: {color: theme.primaryText},
    applied: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      padding: 12,
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 8,
      backgroundColor: theme.surface,
      marginBottom: 6,
    },
    candidate: {flexDirection: 'row', alignItems: 'center', gap: 10, marginBottom: 8},
    relay: {fontSize: 14, fontWeight: '600', color: theme.text},
    meta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
    delete: {marginTop: 28},
    lockInputs: {fontFamily: 'monospace', fontSize: 11, lineHeight: 16, color: theme.text},
  });
}

/**
 * What a flake.lock pins, one line per input: "nixpkgs: github
 * NixOS/nixpkgs @ 5d2b81a (2026-10-01)". Unreadable locks give [].
 */
export function lockInputs(lock: string): string[] {
  try {
    const j = JSON.parse(lock);
    const nodes: Record<string, any> = j?.nodes ?? {};
    return Object.entries(nodes)
      .filter(([name, n]) => name !== (j.root ?? 'root') && n?.locked)
      .map(([name, n]) => {
        const l = n.locked;
        const where =
          l.type === 'github' || l.type === 'gitlab' || l.type === 'sourcehut'
            ? `${l.type} ${l.owner}/${l.repo}`
            : `${l.type} ${l.url ?? l.path ?? ''}`.trim();
        const rev = l.rev ? ` @ ${String(l.rev).slice(0, 7)}` : l.narHash ? ` ${String(l.narHash).slice(0, 19)}…` : '';
        const when = l.lastModified ? ` (${new Date(l.lastModified * 1000).toISOString().slice(0, 10)})` : '';
        return `${name}: ${where}${rev}${when}`;
      })
      .sort();
  } catch {
    return [];
  }
}

/** A relay's flake.lock waiting to be accepted, and what it pins. */
function PendingLock({service, onError}: {service: {id: string; pendingLock?: string; pendingLockFrom?: string; flakeLock: string}; onError: (e: unknown) => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const from = service.pendingLockFrom || 'a relay';
  const inputs = lockInputs(service.pendingLock ?? '');
  const before = new Set(lockInputs(service.flakeLock));
  // Notice puts its children in a Text, so the list and buttons go after it.
  return (
    <>
      <Notice kind="info" title={`A flake.lock from ${from} is waiting for you`}>
        {`${from} locked this service's inputs when it built it. Accepted, every relay running it builds with exactly these; until then each keeps its own. A relay can't change what the others run without you.`}
      </Notice>
      {inputs.length > 0 ? (
        <Text style={[styles.lockInputs, styles.spaced]} selectable>
          {inputs.map(i => `${before.has(i) ? '  ' : '+ '}${i}`).join('\n')}
        </Text>
      ) : null}
      <View style={[styles.row, styles.spaced]}>
        <Button label="Accept" kind="secondary" onPress={() => decideNixLock(service.id, true).catch(onError)} />
        <Button label="Turn down" kind="secondary" onPress={() => decideNixLock(service.id, false).catch(onError)} />
      </View>
    </>
  );
}
