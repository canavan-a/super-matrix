import React from 'react';

import {useStore} from '../store';
import {Hint, Label, MultiChips} from './ui';

/**
 * Which IP groups an allow list includes, under its own entries. An address
 * gets in if it's on the list itself or in any picked group.
 */
export function IPGroupPicker({value, onChange}: {value: string[]; onChange: (ids: string[]) => void}): React.JSX.Element {
  const groups = useStore(s => s.ipGroups);
  return (
    <>
      <Label spaced>IP groups</Label>
      {groups.length === 0 ? (
        <Hint>No IP groups yet. Make one from the IP groups button on the home screen to reuse a list here.</Hint>
      ) : (
        <>
          <MultiChips
            options={groups.map(g => ({value: g.id, label: `${g.name} (${g.entries.length})`}))}
            value={value}
            onChange={onChange}
          />
          <Hint>
            Every address in a picked group is allowed too, as if it were on the list above. Picking a group turns the allow list on even if
            the list above is empty.
          </Hint>
        </>
      )}
    </>
  );
}
