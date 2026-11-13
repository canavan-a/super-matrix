import React, {useState} from 'react';
import {Platform, ScrollView, StyleSheet, Text, View} from 'react-native';

import {ApiError, SchemaOp, SchemaPlan, SchemaProblem} from '../api';
import {Notice} from '../components/Notice';
import {Button, Divider, Hint, Input, Label, Title} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {
  applySchema,
  exportSchema,
  overrideOn,
  planSchema,
  useStore,
} from '../store';
import {Theme, useTheme} from '../theme';

// The document being edited outlives the screen, so stepping away to look
// something up doesn't lose it.
let draft = '';

const KIND: Record<SchemaOp['kind'], string> = {
  domain: 'domain',
  ip_group: 'IP group',
  email_group: 'email group',
  deployment: 'deployment',
  path_rule: 'path rule',
  bridge: 'bridge',
};

const VERB: Record<SchemaOp['action'], string> = {
  create: 'Create',
  update: 'Change',
  delete: 'Delete',
  adopt: 'Take charge of',
};
const MARK: Record<SchemaOp['action'], string> = {
  create: '+',
  update: '~',
  delete: '−',
  adopt: '=',
};

function show(v: unknown): string {
  return v === undefined || v === null ? '(none)' : JSON.stringify(v);
}

/** The problems a schema 400 lists, or the error's message. */
export function problemsOf(e: unknown): SchemaProblem[] {
  if (e instanceof ApiError && Array.isArray(e.body?.errors))
    return e.body.errors as SchemaProblem[];
  return [{path: '', msg: (e as Error).message}];
}

/** Downloads text as a file (web only). */
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], {type: 'application/yaml'}));
  const a = document.createElement('a');
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * The declarative schema, over the air: paste or load a document, plan it to
 * see what would change, and apply exactly that plan.
 */
export function SchemaScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const schema = useStore(s => s.schema);
  const [doc, setDocState] = useState(draft);
  const [plan, setPlan] = useState<SchemaPlan | null>(null);
  const [problems, setProblems] = useState<SchemaProblem[]>([]);
  const [notice, setNotice] = useState<{
    kind: 'success' | 'info';
    text: string;
  } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const setDoc = (text: string) => {
    draft = text;
    setDocState(text);
    // A plan is only good for the document it was made from.
    setPlan(null);
    setNotice(null);
  };

  const run = async (what: string, fn: () => Promise<void>) => {
    setBusy(what);
    setProblems([]);
    try {
      await fn();
    } catch (e) {
      setProblems(problemsOf(e));
    } finally {
      setBusy(null);
    }
  };

  const load = (applied: boolean) =>
    run(applied ? 'applied' : 'current', async () => {
      setDoc(await exportSchema(applied));
      setNotice({
        kind: 'info',
        text: applied
          ? 'Loaded the last applied document.'
          : 'Loaded the server as it is now: every collection in full, no markers. Applying it changes nothing.',
      });
    });

  const doPlan = () =>
    run('plan', async () => {
      setNotice(null);
      setPlan(await planSchema(doc));
    });

  const doApply = () => {
    if (!plan) return;
    const go = () =>
      run('apply', async () => {
        try {
          const out = await applySchema(doc, plan.planHash);
          setPlan(null);
          setNotice({
            kind: 'success',
            text: out.applied
              ? `Applied: schema rev ${out.revision}.`
              : 'Nothing to apply.',
          });
        } catch (e) {
          if (e instanceof ApiError && e.status === 409 && e.body?.plan) {
            setPlan(e.body.plan as SchemaPlan);
            throw new ApiError(
              409,
              'Something changed since the plan. Here is the new one: review it and apply again.',
            );
          }
          throw e;
        }
      });
    if (plan.deletes > 0) {
      platformConfirm(
        'Apply the schema?',
        `This deletes ${plan.deletes} thing${
          plan.deletes === 1 ? '' : 's'
        } (in red in the plan).`,
        'Apply',
        go,
      );
    } else go();
  };

  return (
    <ScrollView
      style={styles.fill}
      contentContainerStyle={styles.container}
      keyboardShouldPersistTaps="handled">
      <Hint>
        {schema?.revision
          ? `Schema rev ${schema.revision}, applied ${
              schema.appliedAt
                ? new Date(schema.appliedAt).toLocaleString()
                : ''
            } by ${schema.appliedBy.replace(/^token:/, 'API token ')}.`
          : 'No schema has been applied yet. A good start: load the server as it is, apply that (it only takes charge of what’s there), then loosen it with "..." and ui: where the app should keep a say.'}
        {overrideOn(schema)
          ? ' Override is on: managed values can be edited in the app.'
          : ''}
      </Hint>
      <View style={styles.row}>
        <Button
          label="Load current state"
          kind="secondary"
          onPress={() => load(false)}
          busy={busy === 'current'}
        />
        {schema?.revision ? (
          <Button
            label="Load applied"
            kind="secondary"
            onPress={() => load(true)}
            busy={busy === 'applied'}
          />
        ) : null}
        {Platform.OS === 'web' && doc ? (
          <Button
            label="Download"
            kind="secondary"
            onPress={() => download('supermatrix.yaml', doc)}
          />
        ) : null}
      </View>

      <Label spaced>Document (YAML or JSON)</Label>
      <Input
        value={doc}
        onChangeText={setDoc}
        multiline
        mono
        placeholder={
          'version: 1\ndeployments:\n  "...": true\n  app.example.com:\n    relay: homelab\n    target_host: 10.0.0.5\n    target_port: 8080'
        }
        style={styles.editor}
        accessibilityLabel="Schema document"
      />
      <View style={styles.row}>
        <Button
          label="Plan"
          onPress={doPlan}
          busy={busy === 'plan'}
          disabled={!doc.trim()}
        />
      </View>

      {problems.length > 0 && (
        <Notice
          kind="error"
          title={
            problems.length === 1
              ? 'There’s a problem'
              : `There are ${problems.length} problems`
          }>
          {problems
            .map(p => (p.path ? `${p.path}: ${p.msg}` : p.msg))
            .join('\n')}
        </Notice>
      )}
      {notice && <Notice kind={notice.kind} title={notice.text} />}

      {plan && (
        <>
          <Divider />
          <Title>Plan</Title>
          {plan.ops.length === 0 ? (
            <Hint>
              {plan.changed
                ? 'Nothing on the server changes. Applying records this document, which may manage things differently (its markers).'
                : 'No changes: the server already matches this document.'}
            </Hint>
          ) : (
            <Hint>
              {plan.creates} to create, {plan.updates} to change, {plan.deletes}{' '}
              to delete.
            </Hint>
          )}
          {plan.ops.map((op, i) => (
            <View key={i} style={styles.op}>
              <Text
                style={[
                  styles.opTitle,
                  op.action === 'delete' && styles.delete,
                  op.action === 'create' && styles.create,
                ]}>
                {MARK[op.action]} {VERB[op.action]} {KIND[op.kind]}{' '}
                <Text style={styles.key}>{op.key}</Text>
              </Text>
              {op.drift ? (
                <Text style={styles.drift}>
                  Changed outside the schema since it was applied; this puts it
                  back.
                </Text>
              ) : null}
              {op.changes?.map(ch => (
                <Text key={ch.field} style={styles.change} selectable>
                  {ch.field}: {show(ch.from)} → {show(ch.to)}
                </Text>
              ))}
            </View>
          ))}
          {plan.changed && (
            <View style={styles.row}>
              <Button
                label={
                  plan.deletes > 0 ? `Apply (deletes ${plan.deletes})` : 'Apply'
                }
                kind={plan.deletes > 0 ? 'danger' : 'primary'}
                onPress={doApply}
                busy={busy === 'apply'}
              />
            </View>
          )}
        </>
      )}
    </ScrollView>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {
      padding: 20,
      paddingBottom: 48,
      maxWidth: 720,
      width: '100%',
      alignSelf: 'center',
    },
    row: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'center',
      gap: 8,
      marginTop: 12,
    },
    editor: {minHeight: 280, textAlignVertical: 'top', fontSize: 13},
    op: {
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 8,
      backgroundColor: theme.surface,
      padding: 10,
      marginTop: 8,
    },
    opTitle: {fontSize: 14, fontWeight: '600', color: theme.text},
    key: {fontFamily: 'monospace', fontWeight: '400'},
    create: {color: theme.success},
    delete: {color: theme.danger},
    drift: {fontSize: 12, color: theme.warning, marginTop: 4},
    change: {
      fontFamily: 'monospace',
      fontSize: 12,
      color: theme.textMuted,
      marginTop: 4,
    },
  });
}
