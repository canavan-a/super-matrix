import React, {useState} from 'react';
import {ScrollView, StyleSheet, Text, View} from 'react-native';

import {IPGroup} from '../api';
import {Notice} from '../components/Notice';
import {Button, Divider, Hint, Input, Label, Title} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {deleteIPGroup, saveIPGroup, useStore} from '../store';
import {Theme, useTheme} from '../theme';

/** One address or CIDR per line (commas work too), blanks dropped. */
export function parseEntries(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map(s => s.trim())
    .filter(Boolean);
}

/**
 * IP groups: named sets of addresses and CIDR ranges. A deployment's or
 * public bridge's allow list can include any of them alongside its own
 * entries, so a list kept in many places is edited once, here.
 */
export function IPGroupsScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const groups = useStore(s => s.ipGroups);
  const [error, setError] = useState<string | null>(null);

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      <Title>New IP group</Title>
      <Hint>
        A named list of addresses and CIDR ranges, like an office's or a VPN's. Add it to any deployment's or public bridge's IP allow list,
        next to that list's own entries; editing the group here updates every list that has it.
      </Hint>
      <GroupForm group={null} onError={setError} />
      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}

      <Divider />
      <Title>IP groups</Title>
      {groups.length === 0 && <Hint>None yet.</Hint>}
      {groups.map(g => (
        <GroupRow key={g.id} g={g} onError={setError} />
      ))}
    </ScrollView>
  );
}

function usedBy(g: IPGroup): string {
  const parts: string[] = [];
  if (g.deployments) parts.push(`${g.deployments} deployment${g.deployments === 1 ? '' : 's'}`);
  if (g.bridges) parts.push(`${g.bridges} bridge${g.bridges === 1 ? '' : 's'}`);
  return parts.length ? `used by ${parts.join(' and ')}` : 'not used yet';
}

function GroupRow({g, onError}: {g: IPGroup; onError: (msg: string | null) => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [editing, setEditing] = useState(false);
  const inUse = g.deployments + g.bridges > 0;

  return (
    <View style={styles.group}>
      <Text style={styles.name}>{g.name}</Text>
      <Text style={styles.meta}>
        {g.entries.length} entr{g.entries.length === 1 ? 'y' : 'ies'} · {usedBy(g)}
      </Text>
      {editing ? (
        <GroupForm group={g} onError={onError} onDone={() => setEditing(false)} />
      ) : (
        <>
          <Text style={styles.entries} selectable>
            {g.entries.join('\n')}
          </Text>
          <View style={[styles.row, styles.spaced]}>
            <Button label="Edit" kind="secondary" onPress={() => setEditing(true)} />
            <Button
              label="Delete"
              kind="secondary"
              icon="trash"
              onPress={() =>
                inUse
                  ? onError(`${g.name} is ${usedBy(g)}: take it off those allow lists first.`)
                  : platformConfirm(`Delete ${g.name}?`, 'No allow list uses it.', 'Delete', () =>
                      deleteIPGroup(g.id).catch(e => onError((e as Error).message)),
                    )
              }
            />
          </View>
        </>
      )}
    </View>
  );
}

/** Create (group null) or edit a group. */
function GroupForm({group, onError, onDone}: {group: IPGroup | null; onError: (msg: string | null) => void; onDone?: () => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [name, setName] = useState(group?.name ?? '');
  const [entries, setEntries] = useState((group?.entries ?? []).join('\n'));
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    onError(null);
    try {
      await saveIPGroup(group?.id ?? null, {name: name.trim(), entries: parseEntries(entries)});
      if (!group) {
        setName('');
        setEntries('');
      }
      onDone?.();
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const used = group ? group.deployments + group.bridges : 0;
  return (
    <View style={styles.spaced}>
      <Label>Name</Label>
      <Input value={name} onChangeText={setName} placeholder="Office" accessibilityLabel="Group name" />
      <Label spaced>Addresses</Label>
      <Input
        value={entries}
        onChangeText={setEntries}
        multiline
        mono
        placeholder={'203.0.113.7\n10.0.0.0/8\n2001:db8::/32'}
        style={styles.multiline}
        accessibilityLabel="Addresses"
      />
      <Hint>
        One address or CIDR range per line.
        {used ? ` Saving changes who can reach the ${used} allow list${used === 1 ? '' : 's'} using it, at once.` : ''}
      </Hint>
      <View style={[styles.row, styles.spaced]}>
        <Button label={group ? 'Save' : 'Add group'} onPress={save} busy={busy} disabled={!name.trim() || !parseEntries(entries).length} />
        {onDone ? <Button label="Cancel" kind="secondary" onPress={onDone} /> : null}
      </View>
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 560, width: '100%', alignSelf: 'center'},
    row: {flexDirection: 'row', alignItems: 'center', gap: 8},
    spaced: {marginTop: 12},
    multiline: {minHeight: 96, textAlignVertical: 'top'},
    group: {
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 10,
      backgroundColor: theme.surface,
      marginBottom: 8,
      padding: 14,
    },
    name: {fontSize: 15, fontWeight: '700', color: theme.text},
    meta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
    entries: {fontFamily: 'monospace', fontSize: 13, color: theme.text, marginTop: 10},
  });
}
