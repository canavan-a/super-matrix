import React, {useCallback, useEffect, useRef, useState} from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import type {NixEvent} from '../api';
import {ManagedNotice, useLock, useResourceLocked} from '../components/Managed';
import {Notice} from '../components/Notice';
import {Button, Chips, Divider, Hint, Input, Label, Title} from '../components/ui';
import {phaseColor, phaseLabel, phaseSentence, ProgressBar} from '../nix/phase';
import {platformConfirm} from '../platformConfirm';
import type {Route} from '../routes';
import {allApplied, appliedAction, appliedEvents, appliedLogs, followLogs, removeApplied, updateApplied, useStore} from '../store';
import {Theme, useTheme} from '../theme';
import {envText, parseEnv} from './NixServiceScreen';

const MAX_LOG = 200_000;

function since(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${Math.round(s / 3600)} h`;
  return `${Math.round(s / 86400)} days`;
}

const shortPath = (p: string): string => p.replace(/^\/nix\/store\/([a-z0-9]{8})[a-z0-9]{24}-/, '/nix/store/$1…-');

/** An applied Nix service: what it's doing on its relay, and what to do about it. */
export function AppliedNixScreen({id, onNavigate}: {id: string; onNavigate: (r: Route) => void}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const found = useStore(s => allApplied(s.nixServices).find(x => x.applied.id === id) ?? null);
  const a = found?.applied;
  const service = found?.service;
  const lock = useLock(a?.managed);
  const pinned = useResourceLocked(a?.managed);
  const [env, setEnv] = useState(envText(a?.env ?? {}));
  const [error, setError] = useState<string | null>(null);
  const [events, setEvents] = useState<NixEvent[]>([]);
  const [openEvent, setOpenEvent] = useState<number | null>(null);
  const [log, setLog] = useState('');
  const [logState, setLogState] = useState<'off' | 'live' | 'build' | string>('off');
  const stopLog = useRef<(() => void) | null>(null);

  const fail = (e: unknown) => setError((e as Error).message);
  const loadEvents = useCallback(() => {
    appliedEvents(id).then(setEvents).catch(() => {});
  }, [id]);
  // The history follows the phase.
  useEffect(loadEvents, [loadEvents, a?.phase, a?.serving]);
  useEffect(() => () => stopLog.current?.(), []);

  if (!a || !service) {
    return (
      <View style={styles.fill}>
        <View style={styles.container}>
          <Hint>It's gone from this relay.</Hint>
          <Button label="Back to Nix services" kind="secondary" onPress={() => onNavigate({name: 'nix'})} style={styles.spaced} />
        </View>
      </View>
    );
  }

  const follow = () => {
    stopLog.current?.();
    setLog('');
    setLogState('live');
    stopLog.current = followLogs(
      a.id,
      t => setLog(l => (l + t).slice(-MAX_LOG)),
      why => setLogState(why),
    );
  };
  const buildLog = () => {
    stopLog.current?.();
    stopLog.current = null;
    setLogState('build');
    appliedLogs(a.id, true)
      .then(setLog)
      .catch(e => setLogState((e as Error).message));
  };

  const act = (action: 'restart' | 'rebuild' | 'rollback') => appliedAction(a.id, action).then(loadEvents).catch(fail);
  const sentence = phaseSentence(a);

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      <ManagedNotice managed={a.managed} noun="Nix service on this relay" />
      <Pressable onPress={() => onNavigate({name: 'nixService', id: service.id})} accessibilityRole="link">
        <Text style={styles.crumb}>{service.name} ›</Text>
      </Pressable>
      <Title>On {a.relayName}</Title>

      <View style={[styles.status, {borderColor: phaseColor(theme, a)}]}>
        <Text style={[styles.phase, {color: phaseColor(theme, a)}]}>
          {phaseLabel(a.phase)}
          <Text style={styles.since}> · {since(a.phaseSince)}</Text>
        </Text>
        {sentence ? <Text style={styles.sentence}>{sentence}</Text> : null}
        {a.phase === 'building' && <ProgressBar p={a.progress} />}
        {a.restarts > 0 && <Text style={styles.detail}>Restarted {a.restarts} times in the last 2 minutes</Text>}
        {!a.online && <Text style={styles.detail}>{a.relayName} is offline: this is what it last said.</Text>}
        {a.serving ? (
          <Text style={styles.detail} selectable>
            Running {shortPath(a.serving)}
            {a.upToDate ? '' : ' (an older build)'}
          </Text>
        ) : null}
        {a.previous ? (
          <Text style={styles.detail} selectable>
            Before: {shortPath(a.previous)}
          </Text>
        ) : null}
      </View>

      {!a.removing && (
        <>
          <Label spaced>Should be</Label>
          <Chips
            options={[
              {value: 'running', label: 'Running'},
              {value: 'stopped', label: 'Stopped'},
            ]}
            value={a.desired}
            onChange={d => updateApplied(a.id, {desired: d}).catch(fail)}
            disabled={lock('desired')}
          />
          <View style={[styles.row, styles.spaced]}>
            <Button label="Restart" kind="secondary" icon="retry" disabled={!a.online || a.desired === 'stopped'} onPress={() => act('restart')} />
            <Button label="Rebuild" kind="secondary" disabled={!a.online} onPress={() => act('rebuild')} />
            {a.previous ? (
              <Button
                label="Roll back"
                kind="secondary"
                disabled={!a.online}
                onPress={() =>
                  platformConfirm(
                    'Roll back?',
                    `${a.relayName} goes back to the build before this one and stays on it until the service changes.`,
                    'Roll back',
                    () => act('rollback'),
                    false,
                  )
                }
              />
            ) : null}
          </View>

          <Label spaced>Environment on {a.relayName}</Label>
          <Input
            value={env}
            onChangeText={setEnv}
            editable={!lock('env')}
            multiline
            mono
            style={styles.multiline}
            placeholder="PORT=3002"
            accessibilityLabel="Environment on this relay"
          />
          <Hint>
            KEY=value lines, over the service's own. Secrets go in /etc/supermatrix/nix-services/{a.slug}.env on {a.relayName} instead.
          </Hint>
          {!lock('env') && (
            <Button
              label="Save environment"
              kind="secondary"
              style={styles.spaced}
              disabled={envText(parseEnv(env)) === envText(a.env)}
              onPress={() => updateApplied(a.id, {env: parseEnv(env)}).catch(fail)}
            />
          )}
        </>
      )}
      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}

      <Divider />
      <Title>Logs</Title>
      <View style={styles.row}>
        <Button label={logState === 'live' ? 'Following…' : 'Follow the journal'} kind="secondary" disabled={!a.online} onPress={follow} />
        <Button label="Last build's log" kind="secondary" disabled={!a.online} onPress={buildLog} />
        {logState !== 'off' && (
          <Button
            label="Close"
            kind="secondary"
            onPress={() => {
              stopLog.current?.();
              stopLog.current = null;
              setLog('');
              setLogState('off');
            }}
          />
        )}
      </View>
      {logState !== 'off' && logState !== 'live' && logState !== 'build' ? <Hint>{logState}</Hint> : null}
      {log ? (
        <ScrollView style={styles.log} nestedScrollEnabled>
          <Text style={styles.logText} selectable>
            {log}
          </Text>
        </ScrollView>
      ) : null}

      <Divider />
      <Title>History</Title>
      {events.length === 0 && <Hint>Nothing yet.</Hint>}
      {events.map(e => (
        <Pressable
          key={e.id}
          style={styles.event}
          onPress={() => e.log && setOpenEvent(o => (o === e.id ? null : e.id))}
          accessibilityRole={e.log ? 'button' : undefined}>
          <Text style={styles.eventHead}>
            <Text style={styles.eventKind}>{phaseLabel(e.kind)}</Text> · {new Date(e.at).toLocaleString()}
            {e.log ? (openEvent === e.id ? ' ▾' : ' ▸') : ''}
          </Text>
          {e.message ? <Text style={styles.eventMsg}>{e.message}</Text> : null}
          {openEvent === e.id && e.log ? (
            <Text style={[styles.logText, styles.eventLog]} selectable>
              {e.log}
            </Text>
          ) : null}
        </Pressable>
      ))}

      {!pinned && (
        <Button
          label={a.removing ? 'Forget it' : `Remove from ${a.relayName}`}
          kind="danger"
          icon="trash"
          style={styles.remove}
          onPress={() =>
            platformConfirm(
              a.removing ? 'Forget it?' : `Remove from ${a.relayName}?`,
              a.removing
                ? `Only if ${a.relayName} is gone for good: whatever it still runs stays there.`
                : `It's stopped and torn down there${service.removeState ? `, with its data in /var/lib/smx/${a.slug}` : ''}. Its env file stays.`,
              a.removing ? 'Forget' : 'Remove',
              () =>
                removeApplied(a.id)
                  .then(() => a.removing && onNavigate({name: 'nixService', id: service.id}))
                  .catch(fail),
            )
          }
        />
      )}
    </ScrollView>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 720, width: '100%', alignSelf: 'center'},
    row: {flexDirection: 'row', alignItems: 'center', gap: 8, flexWrap: 'wrap'},
    spaced: {marginTop: 12},
    crumb: {fontSize: 13, color: theme.textMuted, marginBottom: 4},
    status: {borderWidth: 1, borderRadius: 10, padding: 14, backgroundColor: theme.surface},
    phase: {fontSize: 17, fontWeight: '700'},
    since: {fontSize: 13, fontWeight: '400', color: theme.textMuted},
    sentence: {fontSize: 14, color: theme.text, marginTop: 6, lineHeight: 20},
    detail: {fontSize: 12, color: theme.textMuted, marginTop: 6, fontFamily: 'monospace'},
    multiline: {minHeight: 72, textAlignVertical: 'top'},
    log: {
      maxHeight: 360,
      marginTop: 12,
      padding: 10,
      borderRadius: 6,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.codeBg,
    },
    logText: {fontFamily: 'monospace', fontSize: 11, lineHeight: 16, color: theme.codeText},
    event: {paddingVertical: 8, borderBottomWidth: 1, borderBottomColor: theme.border},
    eventHead: {fontSize: 12, color: theme.textMuted},
    eventKind: {fontWeight: '700', color: theme.text},
    eventMsg: {fontSize: 13, color: theme.text, marginTop: 2},
    eventLog: {marginTop: 6, padding: 8, borderRadius: 6, backgroundColor: theme.codeBg},
    remove: {marginTop: 28},
  });
}
