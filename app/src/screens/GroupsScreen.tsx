import React, {useState} from 'react';
import {ScrollView, StyleSheet, Text, View} from 'react-native';

import type {Managed} from '../api';
import {ManagedBadge, useLock, useLockedEntries, useResourceLocked, useStrict} from '../components/Managed';
import {Notice} from '../components/Notice';
import {Button, Chips, Divider, Hint, Input, Label, Title} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {deleteEmailGroup, deleteIPGroup, saveEmailGroup, saveIPGroup, supports, useStore} from '../store';
import {Theme, useTheme} from '../theme';

export type GroupKind = 'ip' | 'email';

/** One entry per line (commas work too), blanks dropped. */
export function parseEntries(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map(s => s.trim())
    .filter(Boolean);
}

/** What the screen needs of a group, whatever its kind. */
interface Group {
  id: string;
  name: string;
  entries: string[];
  deployments: number;
  /** IP groups only: public bridges that include it */
  bridges?: number;
  managed?: Managed | null;
}

/** What differs between the kinds of group. */
interface KindSpec {
  noun: string;
  intro: string;
  entriesLabel: string;
  placeholder: string;
  entriesHint: string;
  /** what includes the group, in "take it off those … first" */
  lists: string;
  /** the collection in the schema document */
  collection: string;
  save: (id: string | null, input: {name: string; entries: string[]}) => Promise<unknown>;
  remove: (id: string) => Promise<void>;
}

const KINDS: Record<GroupKind, KindSpec> = {
  ip: {
    noun: 'IP group',
    intro:
      "A named list of addresses and CIDR ranges, like an office's or a VPN's. Add it to any deployment's or public bridge's IP allow list, next to that list's own entries; editing the group here updates every list that has it.",
    entriesLabel: 'Addresses',
    placeholder: '203.0.113.7\n10.0.0.0/8\n2001:db8::/32',
    entriesHint: 'One address or CIDR range per line.',
    lists: 'allow lists',
    collection: 'groups.ip',
    save: saveIPGroup,
    remove: deleteIPGroup,
  },
  email: {
    noun: 'email group',
    intro:
      "A named list of people, by email address, like a team. Add it to any email-code deployment's sign-in list, next to that list's own addresses; editing the group here changes who can sign in to all of them.",
    entriesLabel: 'Email addresses',
    placeholder: 'ann@example.com\nbob@example.com\n*@example.org',
    entriesHint: 'One address per line, or *@example.com for everyone at a domain. Taking one off signs that person out everywhere.',
    lists: 'sign-in lists',
    collection: 'groups.email',
    save: saveEmailGroup,
    remove: deleteEmailGroup,
  },
};

/**
 * Groups: named lists kept in one place and included by reference. IP groups
 * go in allow lists; email groups in email-code sign-in lists.
 */
export function GroupsScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const info = useStore(s => s.info);
  const ipGroups = useStore(s => s.ipGroups);
  const emailGroups = useStore(s => s.emailGroups);
  const [picked, setPicked] = useState<GroupKind>('ip');
  const [error, setError] = useState<string | null>(null);

  // A server from before email groups only has IP groups.
  const hasEmail = supports(info, 'email-groups');
  const kind: GroupKind = hasEmail ? picked : 'ip';
  const spec = KINDS[kind];
  const groups: Group[] = kind === 'ip' ? ipGroups : emailGroups;
  const strict = useStrict(spec.collection);

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      {hasEmail && (
        <View style={styles.kinds}>
          <Chips<GroupKind>
            options={[
              {value: 'ip', label: 'IP'},
              {value: 'email', label: 'Email'},
            ]}
            value={kind}
            onChange={k => {
              setPicked(k);
              setError(null);
            }}
          />
        </View>
      )}
      <Title>New {spec.noun}</Title>
      <Hint>{spec.intro}</Hint>
      {strict ? (
        <Hint>The schema lists every {spec.noun}: add new ones there (or turn on override in Settings).</Hint>
      ) : (
        // keyed by kind so switching starts a fresh form
        <GroupForm key={kind} spec={spec} group={null} onError={setError} />
      )}
      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}

      <Divider />
      <Title>{spec.noun[0].toUpperCase() + spec.noun.slice(1)}s</Title>
      {groups.length === 0 && <Hint>None yet.</Hint>}
      {groups.map(g => (
        <GroupRow key={g.id} spec={spec} g={g} onError={setError} />
      ))}
    </ScrollView>
  );
}

function uses(g: Group): number {
  return g.deployments + (g.bridges ?? 0);
}

export function usedBy(g: Group): string {
  const parts: string[] = [];
  if (g.deployments) parts.push(`${g.deployments} deployment${g.deployments === 1 ? '' : 's'}`);
  if (g.bridges) parts.push(`${g.bridges} bridge${g.bridges === 1 ? '' : 's'}`);
  return parts.length ? `used by ${parts.join(' and ')}` : 'not used yet';
}

function GroupRow({spec, g, onError}: {spec: KindSpec; g: Group; onError: (msg: string | null) => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const pinned = useResourceLocked(g.managed);
  const [editing, setEditing] = useState(false);

  return (
    <View style={styles.group}>
      <View style={styles.nameRow}>
        <Text style={styles.name}>{g.name}</Text>
        <ManagedBadge managed={g.managed} />
      </View>
      <Text style={styles.meta}>
        {g.entries.length} entr{g.entries.length === 1 ? 'y' : 'ies'} · {usedBy(g)}
      </Text>
      {editing ? (
        <GroupForm spec={spec} group={g} onError={onError} onDone={() => setEditing(false)} />
      ) : (
        <>
          <Text style={styles.entries} selectable>
            {g.entries.join('\n')}
          </Text>
          <View style={[styles.row, styles.spaced]}>
            <Button label="Edit" kind="secondary" onPress={() => setEditing(true)} />
            {!pinned && (
            <Button
              label="Delete"
              kind="secondary"
              icon="trash"
              onPress={() =>
                uses(g) > 0
                  ? onError(`${g.name} is ${usedBy(g)}: take it off those ${spec.lists} first.`)
                  : platformConfirm(`Delete ${g.name}?`, `No ${spec.lists.replace(/s$/, '')} uses it.`, 'Delete', () =>
                      spec.remove(g.id).catch(e => onError((e as Error).message)),
                    )
              }
            />
            )}
          </View>
        </>
      )}
    </View>
  );
}

/** Create (group null) or edit a group. */
function GroupForm({
  spec,
  group,
  onError,
  onDone,
}: {
  spec: KindSpec;
  group: Group | null;
  onError: (msg: string | null) => void;
  onDone?: () => void;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [name, setName] = useState(group?.name ?? '');
  const [entries, setEntries] = useState((group?.entries ?? []).join('\n'));
  const pinned = useResourceLocked(group?.managed);
  const lock = useLock(group?.managed);
  const schemaEntries = useLockedEntries(group?.managed, 'entries');
  const [busy, setBusy] = useState(false);

  const save = async () => {
    setBusy(true);
    onError(null);
    try {
      await spec.save(group?.id ?? null, {name: name.trim(), entries: parseEntries(entries)});
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

  const used = group ? uses(group) : 0;
  return (
    <View style={styles.spaced}>
      <Label>Name</Label>
      <Input value={name} onChangeText={setName} editable={!pinned} placeholder={spec.noun === 'IP group' ? 'Office' : 'Team'} accessibilityLabel="Group name" />
      <Label spaced>{spec.entriesLabel}</Label>
      <Input
        value={entries}
        onChangeText={setEntries}
        editable={!lock('entries')}
        multiline
        mono
        placeholder={spec.placeholder}
        style={styles.multiline}
        accessibilityLabel={spec.entriesLabel}
      />
      <Hint>
        {spec.entriesHint}
        {used ? ` Saving changes the ${used} ${used === 1 ? spec.lists.replace(/s$/, '') : spec.lists} using it, at once.` : ''}
      </Hint>
      {schemaEntries.length > 0 && <Hint>From the schema, kept whatever you change: {schemaEntries.join(', ')}.</Hint>}
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
    kinds: {marginBottom: 12},
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
    nameRow: {flexDirection: 'row', alignItems: 'center', gap: 6},
    meta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
    entries: {fontFamily: 'monospace', fontSize: 13, color: theme.text, marginTop: 10},
  });
}
