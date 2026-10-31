import React, {useEffect, useState} from 'react';
import {Linking, Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

import {AuthMode, Deployment, DeploymentInput, DeploymentToken, Protocol} from '../api';
import {Icon} from '../components/Icon';
import {transportBadge, viaLine} from '../link';
import {Notice} from '../components/Notice';
import {Button, Chips, CopyBox, Divider, Dot, Hint, Input, Label, Title, ToggleRow} from '../components/ui';
import {platformConfirm} from '../platformConfirm';
import {Route} from '../routes';
import {createToken, deleteDeployment, deleteToken, deploymentHealth, saveDeployment, useStore} from '../store';
import {Theme, useTheme} from '../theme';
import {AUTH_LABEL, healthColor} from './HomeScreen';

export interface DeploymentForm {
  relayId: string;
  domainId: string;
  /** the label under a wildcard domain; unused on an exact one */
  subdomain: string;
  protocol: Protocol;
  targetHost: string;
  targetPort: string;
  tlsSkipVerify: boolean;
  sni: string;
  preserveHost: boolean;
  enabled: boolean;
  authMode: AuthMode;
  /** a new login password; "" keeps the current one */
  loginPassword: string;
  /** drop the current login password (root password only) */
  clearLoginPassword: boolean;
  allowRootLogin: boolean;
  sessionTtlS: number;
  basicUser: string;
  /** a new basic-auth password; "" keeps the current one */
  basicPassword: string;
  /** one email address per line (otp mode) */
  otpEmails: string;
  /** one CIDR or address per line */
  ipAllowlist: string;
  /** ssh: the supermatrix CLI, the browser shell, and the username it suggests */
  sshCli: boolean;
  sshShell: boolean;
  sshUser: string;
}

export function formFrom(d: Deployment | null, relayId = '', domainId = ''): DeploymentForm {
  return {
    relayId: d?.relayId ?? relayId,
    domainId: d?.domainId ?? domainId,
    subdomain: d?.subdomain ?? '',
    protocol: d?.protocol ?? 'http',
    targetHost: d?.targetHost ?? '127.0.0.1',
    targetPort: d ? String(d.targetPort) : '',
    tlsSkipVerify: d?.tlsSkipVerify ?? false,
    sni: d?.sni ?? '',
    preserveHost: d?.preserveHost ?? false,
    enabled: d?.enabled ?? true,
    authMode: d?.authMode ?? 'login',
    loginPassword: '',
    clearLoginPassword: false,
    allowRootLogin: d?.allowRootLogin ?? true,
    sessionTtlS: d?.sessionTtlS ?? 7 * 24 * 3600,
    basicUser: d?.basicUser ?? '',
    basicPassword: '',
    otpEmails: (d?.otpEmails ?? []).join('\n'),
    ipAllowlist: (d?.ipAllowlist ?? []).join('\n'),
    sshCli: d?.sshCli ?? true,
    sshShell: d?.sshShell ?? true,
    sshUser: d?.sshUser ?? '',
  };
}

/** What to send for f. Passwords only go when changed; the server hashes them. */
export function formToInput(f: DeploymentForm): DeploymentInput {
  const input: DeploymentInput = {
    relayId: f.relayId,
    ...(f.domainId ? {domainId: f.domainId} : {}),
    subdomain: f.subdomain.trim().toLowerCase(),
    protocol: f.protocol,
    targetHost: f.targetHost.trim(),
    targetPort: Number(f.targetPort),
    tlsSkipVerify: f.protocol === 'https' && f.tlsSkipVerify,
    sni: f.protocol === 'https' ? f.sni.trim() : '',
    preserveHost: f.protocol !== 'ssh' && f.preserveHost,
    enabled: f.enabled,
    authMode: f.authMode,
    allowRootLogin: f.allowRootLogin,
    sessionTtlS: f.sessionTtlS,
    basicUser: f.basicUser.trim(),
    otpEmails: f.otpEmails
      .split(/[\n,]/)
      .map(s => s.trim())
      .filter(Boolean),
    ipAllowlist: f.ipAllowlist
      .split(/[\n,]/)
      .map(s => s.trim())
      .filter(Boolean),
    sshCli: f.sshCli,
    sshShell: f.sshShell,
    sshUser: f.sshUser.trim(),
  };
  if (f.clearLoginPassword) input.loginPassword = '';
  else if (f.loginPassword) input.loginPassword = f.loginPassword;
  if (f.basicPassword) input.basicPassword = f.basicPassword;
  return input;
}

const AUTH_OPTIONS: {value: AuthMode; label: string}[] = [
  {value: 'login', label: AUTH_LABEL.login},
  {value: 'otp', label: AUTH_LABEL.otp},
  {value: 'basic', label: AUTH_LABEL.basic},
  {value: 'token', label: AUTH_LABEL.token},
  {value: 'none', label: 'Public'},
];

const AUTH_HINT: Record<AuthMode, string> = {
  login: 'Visitors get a Super Matrix sign-in page and a session cookie for this host only.',
  otp:
    "Only addresses on the list get in: they enter their email, get a 6-digit code, and get a session cookie. The service sees who it is in X-Supermatrix-User; its own fetch calls get a 401 with X-Supermatrix-Auth: expired when the session ends.",
  basic: "The browser's own username and password prompt. Handy for scripts and curl -u.",
  token: 'No sign-in page: only requests carrying one of the API tokens below get in.',
  none: 'Anyone with the address can reach it. No password.',
};

const TTL_OPTIONS = [
  {value: 900, label: '15 min'},
  {value: 3600, label: '1 hour'},
  {value: 8 * 3600, label: '8 hours'},
  {value: 24 * 3600, label: '1 day'},
  {value: 7 * 24 * 3600, label: '7 days'},
  {value: 30 * 24 * 3600, label: '30 days'},
];

export function DeploymentScreen({id, onNavigate}: {id: string | null; onNavigate: (r: Route) => void}): React.JSX.Element {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const existing = useStore(s => (id ? s.deployments.find(d => d.id === id) ?? null : null));
  const relays = useStore(s => s.relays);
  const domains = useStore(s => s.domains);
  const live = useStore(s => s.live);
  const info = useStore(s => s.info);
  const [form, setForm] = useState<DeploymentForm>(() =>
    formFrom(existing, relays.length === 1 ? relays[0].id : '', (domains.find(d => d.wildcard) ?? domains[0])?.id ?? ''),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // A deployment deleted elsewhere: back to the list.
  useEffect(() => {
    if (id && !existing) onNavigate({name: 'home'});
  }, [id, existing, onNavigate]);

  const set = (patch: Partial<DeploymentForm>) => {
    setSaved(false);
    setForm(f => ({...f, ...patch}));
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const d = await saveDeployment(id, formToInput(form));
      setForm(formFrom(d));
      setSaved(true);
      if (!id) onNavigate({name: 'deployment', id: d.id});
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = () =>
    platformConfirm(
      `Delete ${existing?.host}?`,
      `${existing?.host} stops working at once, and its tokens are deleted.`,
      'Delete',
      async () => {
        try {
          await deleteDeployment(id!);
          onNavigate({name: 'home'});
        } catch (e) {
          setError((e as Error).message);
        }
      },
    );

  const health = existing ? deploymentHealth(existing, live) : null;
  const domain = domains.find(d => d.id === form.domainId);
  const publicHost = domain ? (domain.wildcard ? `${form.subdomain.trim().toLowerCase() || 'app'}.${domain.name}` : domain.name) : 'app';

  return (
    <ScrollView style={styles.fill} contentContainerStyle={styles.container} keyboardShouldPersistTaps="handled">
      {existing && health && (
        <View style={styles.card}>
          <View style={styles.cardRow}>
            <Dot color={healthColor(theme, health.health)} />
            <Text style={styles.cardHost} numberOfLines={1}>
              {existing.host}
            </Text>
            <Pressable onPress={() => Linking.openURL(existing.url)} accessibilityLabel="Open in browser" hitSlop={8}>
              <Icon name="external" size={18} color={theme.primary} />
            </Pressable>
          </View>
          <Text style={styles.cardText}>{health.detail}</Text>
          <Text style={styles.cardText}>
            {viaLine(relays.find(r => r.id === existing.relayId)?.name ?? 'its relay', live[existing.relayId])}
          </Text>
          {info?.tls && <Text style={styles.cardText}>{certLine(existing)}</Text>}
        </View>
      )}

      <Title>Where</Title>
      <Label>Domain</Label>
      {domains.length === 0 ? (
        <Hint error>Add a domain first.</Hint>
      ) : (
        <View style={styles.relayChips}>
          {domains.map(d => (
            <Pressable
              key={d.id}
              style={[styles.relayChip, form.domainId === d.id && styles.relayChipOn]}
              onPress={() => set({domainId: d.id})}
              accessibilityRole="radio"
              accessibilityState={{selected: form.domainId === d.id}}>
              <Text style={[styles.relayChipText, form.domainId === d.id && styles.relayChipTextOn]}>{d.label}</Text>
            </Pressable>
          ))}
        </View>
      )}
      <Pressable onPress={() => onNavigate({name: 'domains'})} accessibilityRole="link" hitSlop={6}>
        <Text style={styles.link}>Manage domains</Text>
      </Pressable>

      {domain?.wildcard && (
        <>
          <Label spaced>Address</Label>
          <View style={styles.subRow}>
            <Input
              style={styles.flex}
              value={form.subdomain}
              onChangeText={v => set({subdomain: v})}
              placeholder="app"
              accessibilityLabel="Subdomain"
            />
            <Text style={styles.suffix} numberOfLines={1}>
              .{domain.name}
            </Text>
          </View>
        </>
      )}
      {domain && !domain.wildcard && <Hint>This deployment answers on {domain.name} itself. An exact domain holds one deployment.</Hint>}
      <Hint>Gets its own Let's Encrypt certificate the first time it's used.</Hint>

      <Label spaced>Relay</Label>
      {relays.length === 0 ? (
        <Hint error>Add a relay first (Relays, at the top).</Hint>
      ) : (
        <View style={styles.relayChips}>
          {relays.map(r => (
            <Pressable
              key={r.id}
              style={[styles.relayChip, form.relayId === r.id && styles.relayChipOn]}
              onPress={() => set({relayId: r.id})}
              accessibilityRole="radio"
              accessibilityState={{selected: form.relayId === r.id}}>
              <Dot color={live[r.id] ? theme.success : theme.textMuted} />
              <Text style={[styles.relayChipText, form.relayId === r.id && styles.relayChipTextOn]}>{r.name}</Text>
              {live[r.id] ? (
                <Text style={[styles.relayChipMeta, form.relayId === r.id && styles.relayChipMetaOn]}>{transportBadge(live[r.id])}</Text>
              ) : null}
            </Pressable>
          ))}
        </View>
      )}

      <Label spaced>Service, as the relay sees it</Label>
      <Chips
        options={[
          {value: 'http', label: 'http'},
          {value: 'https', label: 'https'},
          {value: 'ssh', label: 'ssh'},
        ]}
        value={form.protocol}
        onChange={v => set(protocolPatch(form, v))}
      />
      <View style={[styles.subRow, styles.spaced]}>
        <Input
          style={styles.flex}
          mono
          value={form.targetHost}
          onChangeText={v => set({targetHost: v})}
          placeholder="127.0.0.1"
          accessibilityLabel="Target host"
        />
        <Text style={styles.colon}>:</Text>
        <Input
          style={styles.port}
          mono
          value={form.targetPort}
          onChangeText={v => set({targetPort: v.replace(/[^0-9]/g, '')})}
          placeholder={form.protocol === 'ssh' ? '22' : '8080'}
          keyboardType="number-pad"
          accessibilityLabel="Target port"
        />
      </View>
      {form.protocol === 'ssh' && (
        <Hint>The sshd the relay connects to, from its own machine: 127.0.0.1:22 for the relay's own, or another host on its network.</Hint>
      )}
      {form.protocol === 'https' && (
        <>
          <ToggleRow
            label="Skip certificate check"
            hint="For a service with a self-signed certificate. The relay still encrypts to it."
            value={form.tlsSkipVerify}
            onChange={v => set({tlsSkipVerify: v})}
          />
          <Label>Server name (SNI)</Label>
          <Input value={form.sni} onChangeText={v => set({sni: v})} placeholder="Defaults to the target host" />
        </>
      )}
      {form.protocol !== 'ssh' && (
        <ToggleRow
          label="Keep the public Host header"
          hint={`Off sends Host: ${form.targetHost || 'target'}${
            form.targetPort ? ':' + form.targetPort : ''
          }. On sends ${publicHost}, for apps that check it.`}
          value={form.preserveHost}
          onChange={v => set({preserveHost: v})}
        />
      )}
      {id && (
        <ToggleRow label="Enabled" hint="Off answers 404 and stops forwarding." value={form.enabled} onChange={v => set({enabled: v})} />
      )}

      {form.protocol === 'ssh' && <SSHAccess form={form} set={set} existing={existing} host={existing?.host ?? publicHost} />}

      <Divider />
      <Title>Who gets in</Title>
      <Chips
        options={form.protocol === 'ssh' ? AUTH_OPTIONS.filter(o => o.value !== 'basic') : AUTH_OPTIONS}
        value={form.authMode}
        onChange={v => set({authMode: v})}
      />
      {form.protocol === 'ssh' && form.authMode === 'none' && (
        <Notice kind="error" title="Anyone can reach this sshd">
          Public means no Super Matrix sign-in in front: it's like opening port 22 to the internet, and only the server's own passwords
          or keys stand in the way. Prefer Login or Email code.
        </Notice>
      )}
      {form.protocol === 'ssh' && form.authMode !== 'none' && (
        <Hint>
          {form.authMode === 'token'
            ? 'The CLI connects with SUPERMATRIX_TOKEN_FILE set to one of the tokens below; the browser shell is unusable.'
            : 'The same sign-in guards the shell and the CLI: `supermatrix access` opens it in the browser, then remembers the session.'}
        </Hint>
      )}
      <Hint>{AUTH_HINT[form.authMode]}</Hint>

      {form.authMode === 'login' && (
        <>
          <Label spaced>Password</Label>
          <Input
            value={form.loginPassword}
            onChangeText={v => set({loginPassword: v, clearLoginPassword: false})}
            secureTextEntry
            placeholder={
              existing?.hasLoginPassword && !form.clearLoginPassword
                ? 'Leave blank to keep the current one'
                : 'A password for this deployment'
            }
            accessibilityLabel="Login password"
          />
          {existing?.hasLoginPassword && !form.clearLoginPassword && !form.loginPassword && (
            <Button label="Remove password" kind="secondary" onPress={() => set({clearLoginPassword: true})} style={styles.spacedSmall} />
          )}
          {form.clearLoginPassword && <Hint>The password is removed when you save; only the root password will work.</Hint>}
          <ToggleRow
            label="Root password works too"
            hint="Sign in here with the server's root password as well."
            value={form.allowRootLogin}
            onChange={v => set({allowRootLogin: v})}
          />
          <Label>Stay signed in for</Label>
          <Chips options={TTL_OPTIONS} value={form.sessionTtlS} onChange={v => set({sessionTtlS: v})} />
        </>
      )}

      {form.authMode === 'otp' && (
        <>
          {info && !info.smtp && (
            <Notice kind="info" title="Email isn't set up on the server">
              Codes can't be sent until the server has SMTP: SUPERMATRIX_SMTP_EMAIL and SUPERMATRIX_SMTP_APP_PASSWORD in its
              environment file.
            </Notice>
          )}
          <Label spaced>Who can sign in</Label>
          <Input
            value={form.otpEmails}
            onChangeText={v => set({otpEmails: v})}
            multiline
            mono
            keyboardType="email-address"
            placeholder={'ann@example.com\nbob@example.com'}
            style={styles.multiline}
            accessibilityLabel="Email addresses that can sign in"
          />
          <Hint>
            One address per line, or *@example.com for everyone at a domain. Taking one off signs that person out.
          </Hint>
          <Label>Stay signed in for</Label>
          <Chips options={TTL_OPTIONS} value={form.sessionTtlS} onChange={v => set({sessionTtlS: v})} />
          <Hint>After this they're asked for a new code.</Hint>
        </>
      )}

      {form.authMode === 'basic' && (
        <>
          <Label spaced>Username</Label>
          <Input
            value={form.basicUser}
            onChangeText={v => set({basicUser: v})}
            placeholder="admin"
            accessibilityLabel="Basic auth username"
          />
          <Label spaced>Password</Label>
          <Input
            value={form.basicPassword}
            onChangeText={v => set({basicPassword: v})}
            secureTextEntry
            placeholder={existing?.hasBasicPassword ? 'Leave blank to keep the current one' : 'At least 8 characters'}
            accessibilityLabel="Basic auth password"
          />
        </>
      )}

      {form.authMode !== 'none' && (
        <>
          <Label spaced>API tokens</Label>
          <Hint>
            {form.authMode === 'token'
              ? 'The only way in: Authorization: Bearer <token>, or an X-Supermatrix-Token header.'
              : 'For scripts and services: a request with a token (Authorization: Bearer <token>, or an X-Supermatrix-Token header) skips the sign-in above.'}
          </Hint>
          {existing ? <Tokens d={existing} tokenOnly={form.authMode === 'token'} /> : <Hint>Save first, then make tokens here.</Hint>}
        </>
      )}

      <Label spaced>IP allow list</Label>
      <Input
        value={form.ipAllowlist}
        onChangeText={v => set({ipAllowlist: v})}
        multiline
        mono
        placeholder={'Anyone (empty)\n203.0.113.7\n10.0.0.0/8'}
        style={styles.multiline}
        accessibilityLabel="IP allow list"
      />
      <Hint>One address or CIDR range per line, checked before any sign-in. Empty lets every address try.</Hint>

      {error ? (
        <Notice kind="error" title="Not saved">
          {error}
        </Notice>
      ) : null}
      {saved && !error ? <Notice kind="success" title="Saved" /> : null}

      <Button label={id ? 'Save' : 'Create deployment'} onPress={save} busy={busy} disabled={!form.relayId} style={styles.save} />

      {id && (
        <>
          <Divider />
          <Button label="Delete deployment" kind="danger" onPress={remove} />
        </>
      )}
    </ScrollView>
  );
}

/** Switching protocol: suggest the usual port, and leave basic auth, which ssh can't use. */
export function protocolPatch(f: DeploymentForm, protocol: Protocol): Partial<DeploymentForm> {
  const patch: Partial<DeploymentForm> = {protocol};
  if (protocol === 'ssh') {
    if (!f.targetPort || f.targetPort === '80' || f.targetPort === '443') patch.targetPort = '22';
    if (f.authMode === 'basic') patch.authMode = 'login';
  } else if (f.protocol === 'ssh' && f.targetPort === '22') {
    patch.targetPort = '';
  }
  return patch;
}

/** ssh's own settings: how to reach it, the shell, and the host key it trusts. */
function SSHAccess({
  form,
  set,
  existing,
  host,
}: {
  form: DeploymentForm;
  set: (patch: Partial<DeploymentForm>) => void;
  existing: Deployment | null;
  host: string;
}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [error, setError] = useState<string | null>(null);

  const resetKey = () =>
    platformConfirm(
      'Forget the host key?',
      'The shell trusts whatever key the server shows next. Only do this if you know why it changed, e.g. the machine was reinstalled.',
      'Forget',
      () => saveDeployment(existing!.id, {sshHostKey: ''}).catch(e => setError((e as Error).message)),
    );

  return (
    <>
      <Divider />
      <Title>SSH access</Title>
      <ToggleRow
        label="Terminal (supermatrix CLI)"
        hint="ssh through the supermatrix CLI as a ProxyCommand. It opens the sign-in below in your browser, then remembers it."
        value={form.sshCli}
        onChange={v => set({sshCli: v})}
      />
      {form.sshCli && (
        <CopyBox text={`Host ${host}\n  ProxyCommand supermatrix access %h`} label="~/.ssh/config" />
      )}
      <ToggleRow
        label="Browser shell"
        hint={`A terminal at https://${host}, behind the sign-in below. It then asks for the server's own username and password; nothing is stored.`}
        value={form.sshShell}
        onChange={v => set({sshShell: v})}
      />
      {form.sshShell && (
        <>
          <Label>Suggested username</Label>
          <Input value={form.sshUser} onChangeText={v => set({sshUser: v})} placeholder="Optional, e.g. root" autoCapitalize="none" />
          <Label spaced>Host key</Label>
          {existing?.sshHostKey ? (
            <View style={styles.subRow}>
              <Text style={[styles.flex, styles.mono]} selectable numberOfLines={2}>
                {existing.sshHostKey}
              </Text>
              <Button label="Forget" kind="secondary" onPress={resetKey} />
            </View>
          ) : (
            <Hint>Saved the first time the shell connects, and checked every time after.</Hint>
          )}
        </>
      )}
      {error ? <Hint error>{error}</Hint> : null}
    </>
  );
}

function certLine(d: Deployment): string {
  switch (d.certStatus) {
    case 'ok':
      return `Certificate valid until ${d.certExpiresAt ? new Date(d.certExpiresAt).toLocaleDateString() : '?'}`;
    case 'error':
      return `Certificate failed: ${d.certError || 'unknown error'}. Check DNS points at the server; it retries every 12 hours.`;
    case 'off':
      return 'TLS is off on this server';
    default:
      return 'Getting a certificate…';
  }
}

/** API tokens for a deployment: list, add (shown once), revoke. */
const TOKEN_EXPIRY_OPTIONS = [
  {value: 0, label: 'Never'},
  {value: 3600, label: '1 hour'},
  {value: 24 * 3600, label: '1 day'},
  {value: 7 * 24 * 3600, label: '7 days'},
  {value: 30 * 24 * 3600, label: '30 days'},
  {value: 90 * 24 * 3600, label: '90 days'},
  {value: 365 * 24 * 3600, label: '1 year'},
];

function tokenMeta(t: DeploymentToken): string {
  const used = t.lastUsedAt ? `last used ${new Date(t.lastUsedAt).toLocaleString()}` : 'never used';
  if (!t.expiresAt) return used;
  const exp = new Date(t.expiresAt);
  return exp.getTime() <= Date.now() ? `expired ${exp.toLocaleString()} · ${used}` : `expires ${exp.toLocaleString()} · ${used}`;
}

function Tokens({d, tokenOnly}: {d: Deployment; tokenOnly: boolean}) {
  const theme = useTheme();
  const styles = makeStyles(theme);
  const [name, setName] = useState('');
  const [expiresInS, setExpiresInS] = useState(0);
  const [fresh, setFresh] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const add = async () => {
    setBusy(true);
    setError(null);
    try {
      setFresh(await createToken(d.id, name.trim(), expiresInS));
      setName('');
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <View style={styles.tokens}>
      {d.tokens.length === 0 && <Hint>{tokenOnly ? 'No tokens yet: nothing can get in.' : 'No tokens yet.'}</Hint>}
      {d.tokens.map(t => (
        <View key={t.id} style={styles.tokenRow}>
          <View style={styles.flex}>
            <Text style={styles.tokenName}>{t.name}</Text>
            <Text style={styles.tokenMeta}>{tokenMeta(t)}</Text>
          </View>
          <Pressable
            onPress={() =>
              platformConfirm(`Revoke ${t.name}?`, 'Anything using this token stops getting in.', 'Revoke', () =>
                deleteToken(d.id, t.id).catch(e => setError((e as Error).message)),
              )
            }
            accessibilityLabel={`Revoke ${t.name}`}
            hitSlop={8}>
            <Icon name="trash" size={18} color={theme.danger} />
          </Pressable>
        </View>
      ))}
      <View style={[styles.subRow, styles.spaced]}>
        <Input style={styles.flex} value={name} onChangeText={setName} placeholder="Token name, e.g. ci" onSubmitEditing={add} />
        <Button label="Add" kind="secondary" onPress={add} busy={busy} disabled={!name.trim()} />
      </View>
      <Label>New token expires</Label>
      <Chips options={TOKEN_EXPIRY_OPTIONS} value={expiresInS} onChange={setExpiresInS} />
      <Hint>An expired token gets a 401 with X-Supermatrix-Auth: token_expired, never the sign-in page. Revoke and add a new one to rotate.</Hint>
      {fresh && (
        <>
          <Notice kind="success" title="New token: copy it now, it won't be shown again" />
          <CopyBox text={fresh} label="token" />
          {d.protocol === 'ssh' ? (
            <CopyBox text={`SUPERMATRIX_TOKEN=${fresh} supermatrix access ${d.host}`} label="CLI example" />
          ) : (
            <CopyBox text={`curl -H "Authorization: Bearer ${fresh}" ${d.url}/`} label="curl example" />
          )}
        </>
      )}
      {error ? <Hint error>{error}</Hint> : null}
    </View>
  );
}

function makeStyles(theme: Theme) {
  return StyleSheet.create({
    fill: {flex: 1, backgroundColor: theme.bg},
    container: {padding: 20, paddingBottom: 48, maxWidth: 560, width: '100%', alignSelf: 'center'},
    card: {
      padding: 14,
      borderRadius: 10,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.surface,
      marginBottom: 20,
      gap: 4,
    },
    cardRow: {flexDirection: 'row', alignItems: 'center', gap: 8},
    cardHost: {flex: 1, fontSize: 15, fontWeight: '700', color: theme.text},
    cardText: {fontSize: 12, color: theme.textMuted, lineHeight: 17},
    subRow: {flexDirection: 'row', alignItems: 'center', gap: 6},
    flex: {flex: 1, minWidth: 0},
    suffix: {fontSize: 14, color: theme.textMuted, fontFamily: 'monospace', flexShrink: 1},
    colon: {fontSize: 16, color: theme.textMuted},
    port: {width: 90},
    spaced: {marginTop: 10},
    spacedSmall: {marginTop: 8},
    relayChips: {flexDirection: 'row', flexWrap: 'wrap', gap: 8},
    link: {fontSize: 13, color: theme.primary, fontWeight: '600', marginTop: 8},
    relayChip: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 6,
      paddingHorizontal: 12,
      paddingVertical: 8,
      borderRadius: 6,
      backgroundColor: theme.surfaceAlt,
    },
    relayChipOn: {backgroundColor: theme.primary},
    relayChipText: {fontWeight: '600', fontSize: 13, color: theme.text},
    relayChipMeta: {fontSize: 11, color: theme.textMuted},
    relayChipMetaOn: {color: theme.primaryText, opacity: 0.85},
    relayChipTextOn: {color: theme.primaryText},
    multiline: {minHeight: 76, textAlignVertical: 'top'},
    mono: {fontFamily: 'monospace', fontSize: 12, color: theme.text},
    save: {marginTop: 20},
    tokens: {marginTop: 4},
    tokenRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      paddingVertical: 8,
      borderBottomWidth: 1,
      borderBottomColor: theme.border,
    },
    tokenName: {fontSize: 14, fontWeight: '600', color: theme.text},
    tokenMeta: {fontSize: 11, color: theme.textMuted, marginTop: 2},
  });
}
