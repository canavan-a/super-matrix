// How the app shows an applied Nix service's phase: a word, a color, a
// sentence, and a progress bar while it builds.
import React from 'react';
import {StyleSheet, Text, View} from 'react-native';

import type {AppliedNixService, NixPhase, NixProgress} from '../api';
import {Dot} from '../components/ui';
import {Theme, useTheme} from '../theme';

const LABEL: Record<NixPhase, string> = {
  pending: 'pending',
  preparing: 'preparing',
  building: 'building',
  starting: 'starting',
  running: 'running',
  stopping: 'stopping',
  stopped: 'stopped',
  'missing-env': 'needs env',
  'build-failed': 'build failed',
  'start-failed': "won't start",
  crashed: 'crashed',
  'crash-loop': 'crash loop',
  'tearing-down': 'removing',
  removed: 'removed',
  'removed-with-errors': 'removed (errors)',
};

export const phaseLabel = (p: NixPhase | string): string => LABEL[p as NixPhase] ?? p;

export function phaseColor(theme: Theme, a: Pick<AppliedNixService, 'phase' | 'failing'>): string {
  if (a.failing) return theme.danger;
  switch (a.phase) {
    case 'running':
      return theme.success;
    case 'stopped':
    case 'removed':
      return theme.textMuted;
    default:
      return theme.warning;
  }
}

/** "3/12 · nodejs-20.11.1 · 1m 34s", or the step. */
export function progressLine(p: NixProgress | null | undefined): string {
  if (!p) return '';
  const parts: string[] = [];
  if (p.toBuild > 0) parts.push(`built ${p.built}/${p.toBuild}`);
  else if (p.toDownload > 0) parts.push(`fetched ${p.downloaded}/${p.toDownload}`);
  else parts.push(p.step);
  if (p.current) parts.push(p.current);
  if (p.elapsedS > 0) parts.push(p.elapsedS >= 60 ? `${Math.floor(p.elapsedS / 60)}m ${p.elapsedS % 60}s` : `${p.elapsedS}s`);
  return parts.join(' · ');
}

/** What an applied service is doing, in a sentence. */
export function phaseSentence(a: AppliedNixService): string {
  const where = a.relayName || 'the relay';
  if (a.removing && a.phase === 'tearing-down') return a.online ? `Being removed from ${where}` : `Removed once ${where} is back online`;
  switch (a.phase) {
    case 'pending':
      return a.online ? `Waiting for ${where} to pick it up` : `Starts once ${where} is online`;
    case 'building':
      return a.serving ? 'Building an update; the current version keeps running' : 'Building';
    case 'missing-env':
      return `Set ${a.missingEnv.join(', ')} in /etc/supermatrix/nix-services/${a.slug}.env on ${where}`;
    case 'build-failed':
      return a.serving ? 'The latest build failed; the previous version keeps running' : 'The build failed';
    default:
      return a.lastError || '';
  }
}

export function PhaseBadge({a}: {a: AppliedNixService}): React.JSX.Element {
  const theme = useTheme();
  const s = styles(theme);
  return (
    <View style={s.badge}>
      <Dot color={phaseColor(theme, a)} />
      <Text style={[s.badgeText, a.failing && {color: theme.danger}]}>
        {phaseLabel(a.phase)}
        {a.phase === 'building' && a.progress?.toBuild ? ` ${a.progress.built}/${a.progress.toBuild}` : ''}
      </Text>
    </View>
  );
}

/** A bar for built (or fetched) out of expected; indeterminate without counts. */
export function ProgressBar({p}: {p: NixProgress | null | undefined}): React.JSX.Element | null {
  const theme = useTheme();
  const s = styles(theme);
  if (!p) return null;
  const total = p.toBuild || p.toDownload;
  const done = p.toBuild ? p.built : p.downloaded;
  const frac = total > 0 ? Math.min(1, done / total) : 0.15;
  return (
    <View>
      <View style={s.track}>
        <View style={[s.fill, {width: `${Math.max(4, frac * 100)}%`}]} />
      </View>
      <Text style={s.progress}>{progressLine(p)}</Text>
    </View>
  );
}

function styles(theme: Theme) {
  return StyleSheet.create({
    badge: {flexDirection: 'row', alignItems: 'center', gap: 5},
    badgeText: {fontSize: 12, fontWeight: '600', color: theme.text},
    track: {height: 6, borderRadius: 3, backgroundColor: theme.surfaceAlt, overflow: 'hidden', marginTop: 8},
    fill: {height: 6, borderRadius: 3, backgroundColor: theme.primary},
    progress: {fontSize: 12, color: theme.textMuted, marginTop: 4},
  });
}
