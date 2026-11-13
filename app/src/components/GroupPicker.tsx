import React from 'react';

import type {GroupKind} from '../screens/GroupsScreen';
import {useStore} from '../store';
import {Hint, Label, MultiChips} from './ui';

const TEXT: Record<GroupKind, {label: string; none: string; hint: string}> = {
  ip: {
    label: 'IP groups',
    none: 'No IP groups yet. Make one from the groups button on the home screen to reuse a list here.',
    hint:
      'Every address in a picked group is allowed too, as if it were on the list above. Picking a group turns the allow list on even if the list above is empty.',
  },
  email: {
    label: 'Email groups',
    none: 'No email groups yet. Make one from the groups button on the home screen to reuse a list of people here.',
    hint: 'Everyone in a picked group can sign in too, as if they were on the list above. A group alone is enough; the list above can stay empty.',
  },
};

/**
 * Which groups of a kind a list includes, under its own entries: IP groups
 * for an allow list, email groups for a sign-in list.
 */
export function GroupPicker({
  kind,
  value,
  onChange,
  disabled,
}: {
  kind: GroupKind;
  value: string[];
  onChange: (ids: string[]) => void;
  /** shown but not changeable (the schema manages it) */
  disabled?: boolean;
}): React.JSX.Element {
  const ipGroups = useStore(s => s.ipGroups);
  const emailGroups = useStore(s => s.emailGroups);
  const groups = kind === 'ip' ? ipGroups : emailGroups;
  const text = TEXT[kind];
  return (
    <>
      <Label spaced>{text.label}</Label>
      {groups.length === 0 ? (
        <Hint>{text.none}</Hint>
      ) : (
        <>
          <MultiChips
            options={groups.map(g => ({value: g.id, label: `${g.name} (${g.entries.length})`}))}
            value={value}
            onChange={onChange}
            disabled={disabled}
          />
          <Hint>{text.hint}</Hint>
        </>
      )}
    </>
  );
}
