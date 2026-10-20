import React from 'react';
import {ActivityIndicator, StyleSheet, Text, View} from 'react-native';

import {Theme, useTheme} from '../theme';
import {Icon} from './Icon';

export type NoticeKind = 'success' | 'error' | 'working' | 'info';

/** A status card: what just happened, in a color that says how it went. */
export function Notice({kind, title, children}: {kind: NoticeKind; title: string; children?: React.ReactNode}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const color = kind === 'success' ? theme.success : kind === 'error' ? theme.danger : theme.textMuted;
  return (
    <View style={[styles.card, kind === 'error' && styles.cardError, kind === 'success' && {borderColor: theme.success}]}>
      <View style={styles.row}>
        {kind === 'working' ? (
          <ActivityIndicator size="small" color={color} />
        ) : (
          <Icon name={kind === 'success' ? 'check' : kind === 'error' ? 'close' : 'shield'} size={16} color={color} />
        )}
        <Text style={[styles.title, {color}]}>{title}</Text>
      </View>
      {children ? <Text style={styles.body}>{children}</Text> : null}
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    card: {
      marginTop: 12,
      padding: 12,
      borderRadius: 8,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.surface,
      gap: 4,
    },
    cardError: {borderColor: theme.danger, backgroundColor: theme.dangerBg},
    row: {flexDirection: 'row', alignItems: 'center', gap: 8},
    title: {fontSize: 14, fontWeight: '700', flexShrink: 1},
    body: {fontSize: 13, lineHeight: 18, color: theme.text},
  });
}
