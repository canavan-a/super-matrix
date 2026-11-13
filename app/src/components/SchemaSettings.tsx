import React, {useCallback, useEffect, useState} from 'react';
import {StyleSheet, Text, View} from 'react-native';

import type {ApiToken} from '../api';
import {platformConfirm} from '../platformConfirm';
import {
  createApiToken,
  deleteApiToken,
  listApiTokens,
  overrideEndsAt,
  overrideOn,
  setOverride,
  supports,
  useStore,
} from '../store';
import {Theme, useTheme} from '../theme';
import {Notice} from './Notice';
import {
  Button,
  Chips,
  CopyBox,
  Divider,
  Hint,
  Input,
  Label,
  Title,
  ToggleRow,
} from './ui';

const DURATIONS = [
  {value: 60, label: '1 hour'},
  {value: 240, label: '4 hours'},
  {value: 1440, label: '1 day'},
];

function when(iso: string | null): string {
  return iso ? new Date(iso).toLocaleString() : 'never';
}

/**
 * Settings for the declarative schema: override mode, which unlocks what a
 * schema manages for an emergency fix, and API tokens for CI. Hidden on a
 * server without them.
 */
export function SchemaSettings({
  onOpenSchema,
}: {
  onOpenSchema: () => void;
}): React.JSX.Element | null {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const info = useStore(s => s.info);
  const schema = useStore(s => s.schema);
  const [minutes, setMinutes] = useState(60);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [tokens, setTokens] = useState<ApiToken[] | null>(null);
  const [tokenName, setTokenName] = useState('');
  const [made, setMade] = useState<{name: string; token: string} | null>(null);

  const hasTokens = supports(info, 'api-tokens');
  const loadTokens = useCallback(() => {
    if (!hasTokens) return;
    listApiTokens()
      .then(setTokens)
      .catch(e => setError((e as Error).message));
  }, [hasTokens]);
  useEffect(loadTokens, [loadTokens]);

  if (!supports(info, 'schema')) return null;
  const on = overrideOn(schema);

  const toggle = (next: boolean) => {
    const run = () => {
      setBusy(true);
      setError(null);
      setOverride(next, next ? minutes : undefined)
        .catch(e => setError((e as Error).message))
        .finally(() => setBusy(false));
    };
    if (next) {
      platformConfirm(
        'Override the schema?',
        'Everything the schema manages becomes editable here until override ends. The next schema apply puts the schema’s values back.',
        'Override',
        run,
        false,
      );
    } else run();
  };

  const makeToken = async () => {
    setBusy(true);
    setError(null);
    try {
      const out = await createApiToken(tokenName.trim());
      setMade({name: out.apiToken.name, token: out.token});
      setTokenName('');
      loadTokens();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <Divider />
      <Title>Schema</Title>
      <Hint>
        {schema?.revision
          ? `Schema rev ${schema.revision}, applied ${when(
              schema.appliedAt,
            )} by ${schema.appliedBy.replace(/^token:/, 'API token ')}.`
          : 'No schema has been applied: everything here is managed in the app.'}
      </Hint>
      <View style={styles.row}>
        <Button
          label="Open schema"
          kind="secondary"
          icon="code"
          onPress={onOpenSchema}
        />
      </View>

      {schema?.revision ? (
        <>
          <ToggleRow
            label="Override schema locks"
            hint={
              on && overrideEndsAt(schema) !== null
                ? `On until ${new Date(
                    overrideEndsAt(schema)!,
                  ).toLocaleString()}. Values the schema manages can be edited here; the next apply puts them back.`
                : 'For an emergency fix: unlocks values the schema manages, for a while.'
            }
            value={on}
            onChange={busy ? () => {} : toggle}
          />
          {!on && (
            <Chips options={DURATIONS} value={minutes} onChange={setMinutes} />
          )}
        </>
      ) : null}

      {hasTokens && (
        <>
          <Label spaced>API tokens</Label>
          <Hint>
            For CI and scripts: a token can plan, apply and export the schema,
            and nothing else. Changing the root password doesn’t revoke them;
            delete one to revoke it.
          </Hint>
          {tokens?.map(t => (
            <View key={t.id} style={styles.token}>
              <View style={styles.flex}>
                <Text style={styles.tokenName}>{t.name}</Text>
                <Text style={styles.tokenMeta}>
                  made {when(t.createdAt)} · last used {when(t.lastUsedAt)}
                </Text>
              </View>
              <Button
                label="Delete"
                kind="secondary"
                icon="trash"
                onPress={() =>
                  platformConfirm(
                    `Delete ${t.name}?`,
                    'Anything using it stops working.',
                    'Delete',
                    () =>
                      deleteApiToken(t.id)
                        .then(loadTokens)
                        .catch(e => setError((e as Error).message)),
                  )
                }
              />
            </View>
          ))}
          <View style={[styles.row, styles.spaced]}>
            <Input
              style={styles.flex}
              value={tokenName}
              onChangeText={setTokenName}
              placeholder="e.g. GitHub Actions"
              accessibilityLabel="Token name"
            />
            <Button
              label="Make token"
              kind="secondary"
              icon="key"
              onPress={makeToken}
              busy={busy}
              disabled={!tokenName.trim()}
            />
          </View>
          {made && (
            <>
              <Hint>{made.name}: copy it now, it isn’t shown again.</Hint>
              <CopyBox text={made.token} label="API token" />
            </>
          )}
        </>
      )}
      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}
    </>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    row: {flexDirection: 'row', alignItems: 'center', gap: 8, marginTop: 10},
    spaced: {marginTop: 12},
    flex: {flex: 1, minWidth: 0},
    token: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 8,
      paddingVertical: 8,
      borderBottomWidth: 1,
      borderBottomColor: theme.border,
    },
    tokenName: {fontSize: 14, fontWeight: '600', color: theme.text},
    tokenMeta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
  });
}
