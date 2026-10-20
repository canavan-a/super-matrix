import React, {useCallback, useEffect, useState} from 'react';
import {ScrollView, StyleSheet, Text, View} from 'react-native';

import {Domain, DomainDns} from '../api';
import {Notice} from '../components/Notice';
import {Button, CopyBox, Divider, Dot, Hint, Input, Title} from '../components/ui';
import {dnsRecords, isWildcardInput} from '../domains';
import {platformConfirm} from '../platformConfirm';
import {checkDomainDns, createDomain, deleteDomain, useStore} from '../store';
import {Theme, useTheme} from '../theme';

/** The domains deployments can live on: add (wildcard or exact), check DNS, delete. */
export function DomainsScreen(): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const domains = useStore(s => s.domains);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const add = async () => {
    setBusy(true);
    setError(null);
    try {
      await createDomain(name.trim());
      setName('');
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      <Title>Add a domain</Title>
      <Hint>
        A wildcard domain like *.example.com gives each deployment its own subdomain (grafana.example.com). An exact one like
        status.example.com is one deployment's whole address. Either way, point it at this server in DNS; each deployment gets its own
        certificate.
      </Hint>
      <View style={[styles.row, styles.spaced]}>
        <Input
          style={styles.flex}
          value={name}
          onChangeText={setName}
          placeholder="*.example.com or status.example.com"
          autoCapitalize="none"
          autoCorrect={false}
          onSubmitEditing={add}
          accessibilityLabel="Domain"
        />
        <Button label="Add" onPress={add} busy={busy} disabled={!name.trim()} />
      </View>
      {name.trim() ? (
        <Hint>
          {isWildcardInput(name)
            ? 'Wildcard: deployments get a subdomain under it.'
            : 'Exact: one deployment takes this whole name. Start with *. for a wildcard.'}
        </Hint>
      ) : null}
      {error ? (
        <Notice kind="error" title="Couldn't do that">
          {error}
        </Notice>
      ) : null}

      <Divider />
      <Title>Domains</Title>
      {domains.length === 0 && <Hint>None yet. Add one above before creating deployments.</Hint>}
      {domains.map(d => (
        <DomainRow key={d.id} d={d} onError={setError} />
      ))}
    </ScrollView>
  );
}

function DomainRow({d, onError}: {d: Domain; onError: (msg: string) => void}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [dns, setDns] = useState<DomainDns | null>(null);
  const [checking, setChecking] = useState(false);

  const check = useCallback(async () => {
    setChecking(true);
    try {
      setDns(await checkDomainDns(d.id));
    } catch (e) {
      setDns({ok: false, probe: d.name, addresses: [], expected: [], detail: (e as Error).message});
    } finally {
      setChecking(false);
    }
  }, [d.id, d.name]);

  useEffect(() => {
    check();
  }, [check]);

  const color = !dns ? theme.textMuted : dns.ok ? theme.success : theme.danger;
  return (
    <View style={styles.domain}>
      <View style={styles.head}>
        <Dot color={color} />
        <View style={styles.flex}>
          <Text style={styles.name}>{d.label}</Text>
          <Text style={styles.meta}>
            {d.wildcard ? 'wildcard' : 'exact'} · {d.deployments} deployment{d.deployments === 1 ? '' : 's'}
          </Text>
        </View>
      </View>
      <Hint error={!!dns && !dns.ok}>{dns ? dns.detail : 'Checking DNS…'}</Hint>
      {dns && !dns.ok && dns.expected.length > 0 && (
        <>
          <Hint>Add this at your DNS provider:</Hint>
          <CopyBox text={dnsRecords(d.label, dns.expected)} />
        </>
      )}
      <View style={[styles.row, styles.spaced]}>
        <Button label="Check DNS" kind="secondary" icon="retry" busy={checking} onPress={check} />
        <Button
          label="Delete"
          kind="secondary"
          icon="trash"
          onPress={() =>
            platformConfirm(`Delete ${d.label}?`, 'Deployments on it have to be moved or deleted first.', 'Delete', () =>
              deleteDomain(d.id).catch(e => onError((e as Error).message)),
            )
          }
        />
      </View>
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 560, width: '100%', alignSelf: 'center'},
    row: {flexDirection: 'row', alignItems: 'center', gap: 8},
    flex: {flex: 1, minWidth: 0},
    spaced: {marginTop: 12},
    domain: {
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 10,
      backgroundColor: theme.surface,
      marginBottom: 8,
      padding: 14,
    },
    head: {flexDirection: 'row', alignItems: 'center', gap: 10, marginBottom: 6},
    name: {fontSize: 15, fontWeight: '700', color: theme.text},
    meta: {fontSize: 12, color: theme.textMuted, marginTop: 2},
  });
}
