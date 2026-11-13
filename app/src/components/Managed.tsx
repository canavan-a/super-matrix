import React from 'react';
import {StyleSheet, Text, View} from 'react-native';

import type {Managed} from '../api';
import {
  locked,
  lockedEntries,
  overrideOn,
  strictCollection,
  useStore,
} from '../store';
import {Theme, useTheme} from '../theme';
import {Icon} from './Icon';
import {Notice} from './Notice';

// How the app shows what a schema manages: a badge on list rows, a notice
// on edit screens, and locks on the inputs for managed values (the server
// refuses changing them anyway; this says so up front).

/** Readable names for schema document fields. */
const FIELD_LABEL: Record<string, string> = {
  relay: 'relay',
  protocol: 'protocol',
  target_host: 'target host',
  target_port: 'target port',
  tls_skip_verify: 'certificate check',
  sni: 'SNI',
  preserve_host: 'Host header',
  enabled: 'on/off',
  auth_mode: 'sign-in',
  login_password: 'login password',
  allow_root_login: 'root password',
  session_ttl_s: 'session length',
  basic_user: 'basic auth user',
  basic_password: 'basic auth password',
  otp_emails: 'sign-in list',
  email_groups: 'email groups',
  ip_allowlist: 'IP allow list',
  ip_groups: 'IP groups',
  ssh_cli: 'SSH terminal',
  ssh_shell: 'browser shell',
  ssh_user: 'SSH user',
  rate_limit_enabled: 'rate limiting',
  rate_limit_requests: 'rate limit',
  rate_limit_period_s: 'rate limit period',
  rate_limit_burst: 'rate limit burst',
  path_rules: 'path rules',
  name: 'name',
  public: 'public',
  from_relay: 'listening relay',
  listen_host: 'listen address',
  listen_port: 'listen port',
  to_relay: 'target relay',
  udp_idle_s: 'UDP idle timeout',
  domain: 'domain',
  subdomain: 'subdomain',
  entries: 'entries',
};

export const fieldLabel = (f: string): string =>
  FIELD_LABEL[f] ?? f.replace(/_/g, ' ');

/** A small "schema" chip with a lock, for list rows of managed resources. */
export function ManagedBadge({
  managed,
}: {
  managed: Managed | null | undefined;
}): React.JSX.Element | null {
  const theme = useTheme();
  const styles = makeStyles(theme);
  if (!managed) return null;
  return (
    <View
      style={styles.badge}
      accessibilityLabel={`Managed by schema rev ${managed.revision}`}>
      <Icon name="lock" size={11} color={theme.textMuted} />
      <Text style={styles.badgeText}>schema</Text>
    </View>
  );
}

/** Says what the schema manages of a resource, on its edit screen. */
export function ManagedNotice({
  managed,
  noun,
}: {
  managed: Managed | null | undefined;
  noun: string;
}): React.JSX.Element | null {
  const schema = useStore(s => s.schema);
  if (!managed) return null;
  const fields = managed.fields.map(fieldLabel);
  const lists = Object.entries(managed.entries).map(
    ([f, es]) => `${fieldLabel(f)} (${es.join(', ')})`,
  );
  const parts = [
    fields.length ? `It sets the ${fields.join(', ')}.` : '',
    lists.length
      ? `It keeps its own entries in the ${lists.join(
          '; ',
        )}; you can add others.`
      : '',
  ].filter(Boolean);
  return (
    <Notice kind="info" title={`Managed by schema rev ${managed.revision}`}>
      {overrideOn(schema)
        ? `Override is on: you can change anything here, and the next schema apply puts the schema's values back.`
        : `This ${noun} comes from the schema, so it can't be deleted or renamed here. ${parts.join(
            ' ',
          )} Change those in the schema, or turn on override in Settings for an emergency fix.`}
    </Notice>
  );
}

/** lock(field): whether a field of a resource is the schema's (and override is off). */
export function useLock(
  managed: Managed | null | undefined,
): (field: string) => boolean {
  const schema = useStore(s => s.schema);
  return (field: string) => locked(managed, field, schema);
}

/** Whether a managed resource is locked as a whole (its name, deleting it). */
export function useResourceLocked(
  managed: Managed | null | undefined,
): boolean {
  const schema = useStore(s => s.schema);
  return !!managed && !overrideOn(schema);
}

/** The schema's own entries of a list, which can't be removed here. */
export function useLockedEntries(
  managed: Managed | null | undefined,
  field: string,
): string[] {
  const schema = useStore(s => s.schema);
  return lockedEntries(managed, field, schema);
}

/** Whether nothing can be added to a collection (the schema lists it in full). */
export function useStrict(collection: string): boolean {
  const schema = useStore(s => s.schema);
  return strictCollection(schema, collection);
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    badge: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 3,
      paddingHorizontal: 6,
      paddingVertical: 2,
      borderRadius: 4,
      borderWidth: 1,
      borderColor: theme.border,
      alignSelf: 'center',
    },
    badgeText: {
      fontSize: 10,
      fontWeight: '700',
      color: theme.textMuted,
      letterSpacing: 0.3,
    },
  });
}
