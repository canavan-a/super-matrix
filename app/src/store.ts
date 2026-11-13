// The app's state: which server is open, its session, and what's on it
// (relays, deployments, live relay status). Lists come over HTTP; a
// WebSocket to /v1/ws pushes live relay status and says when to refetch.
// Screens read it through useStore(); every change calls emit().
import {useEffect, useReducer, useRef} from 'react';

import {
  api,
  ApiError,
  Bridge,
  BridgeInput,
  BridgeProtocol,
  Deployment,
  DeploymentInput,
  Domain,
  DomainDns,
  EmailGroup,
  EmailGroupInput,
  IPGroup,
  IPGroupInput,
  PathRule,
  PathRuleInput,
  Relay,
  RelayState,
  SchemaPlan,
  SchemaStatus,
  ServerInfo,
  ApiToken,
} from './api';
import {loadSessions, saveSessions} from './keystore';
import {normalizeServer, settingsStore} from './settings';

export type Phase = 'loading' | 'signedOut' | 'ready';

interface State {
  phase: Phase;
  server: string | null;
  token: string | null;
  info: ServerInfo | null;
  relays: Relay[];
  deployments: Deployment[];
  bridges: Bridge[];
  domains: Domain[];
  ipGroups: IPGroup[];
  /** empty against a server without email groups */
  emailGroups: EmailGroup[];
  /** the schema's last apply and override mode; null against a server without schemas */
  schema: SchemaStatus | null;
  /** relay id → live state; a relay missing here is offline */
  live: Record<string, RelayState>;
  /** public bridge id → "ok" or why the server can't listen on its port */
  publicLive: Record<string, string>;
  /** the lists have been fetched at least once for this server */
  loaded: boolean;
  /** the live WebSocket is up */
  connected: boolean;
  /** why the last sign-in or refresh failed, for the screen to show */
  error: string | null;
  /** a sign-in that ended (expired, password changed), shown on the sign-in screen */
  signedOutReason: string | null;
  /** the server "Add a server" left, for the sign-in screen's back arrow */
  returnTo: string | null;
}

const initial: State = {
  phase: 'loading',
  server: null,
  token: null,
  info: null,
  relays: [],
  deployments: [],
  bridges: [],
  domains: [],
  ipGroups: [],
  emailGroups: [],
  schema: null,
  live: {},
  publicLive: {},
  loaded: false,
  connected: false,
  error: null,
  signedOutReason: null,
  returnTo: null,
};

let state: State = initial;
const listeners = new Set<() => void>();
let sessions: Record<string, string> = {};

function set(patch: Partial<State>) {
  state = {...state, ...patch};
  emit();
}

function emit() {
  listeners.forEach(l => l());
}

export const getState = (): State => state;

export function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useStore<T>(selector: (s: State) => T): T {
  const [, force] = useReducer((n: number) => n + 1, 0);
  const sel = useRef(selector);
  sel.current = selector;
  useEffect(() => subscribe(force), []);
  return sel.current(state);
}

/** On launch: reopen the last server if its session is still saved. */
export async function init(): Promise<void> {
  const s = await settingsStore.load();
  sessions = await loadSessions();
  const server = s.activeServer;
  if (server && sessions[server]) {
    open(server, sessions[server]);
  } else {
    set({phase: 'signedOut', server: server ?? s.servers[0] ?? null});
  }
}

function open(server: string, token: string) {
  disconnect();
  set({...initial, phase: 'ready', server, token});
  refresh().catch(() => {});
  connect();
}

/** Trades the root password for a session on server and opens it. */
export async function signIn(serverInput: string, password: string): Promise<void> {
  const server = normalizeServer(serverInput);
  if (!server) throw new ApiError(0, 'Enter your server’s address');
  if (!(await api.health(server))) {
    throw new ApiError(0, `${server.replace(/^https?:\/\//, '')} isn’t answering as a Super Matrix server`);
  }
  const {token} = await api.login(server, password);
  sessions = {...sessions, [server]: token};
  await saveSessions(sessions);
  const s = settingsStore.get();
  await settingsStore.update({
    servers: s.servers.includes(server) ? s.servers : [...s.servers, server],
    activeServer: server,
  });
  open(server, token);
}

/** Opens another saved server, or the sign-in screen for it. */
export async function switchServer(server: string): Promise<void> {
  await settingsStore.update({activeServer: server});
  if (sessions[server]) open(server, sessions[server]);
  else {
    disconnect();
    set({...initial, phase: 'signedOut', server});
  }
}

/** The sign-in screen for another server; saved sessions stay. */
export function showSignIn(): void {
  const from = state.phase === 'ready' ? state.server : null;
  disconnect();
  set({...initial, phase: 'signedOut', server: null, returnTo: from});
}

/** Back from "Add a server" to the server it was opened from. */
export function cancelSignIn(): boolean {
  const {returnTo} = state;
  if (!returnTo || !sessions[returnTo]) return false;
  switchServer(returnTo).catch(() => {});
  return true;
}

/** Ends the session on this device (and on the server, if it answers). */
export async function signOut(reason: string | null = null): Promise<void> {
  const {server, token} = state;
  disconnect();
  if (server) {
    const {[server]: _, ...rest} = sessions;
    sessions = rest;
    await saveSessions(sessions);
  }
  await settingsStore.update({activeServer: null});
  set({...initial, phase: 'signedOut', server, signedOutReason: reason});
  if (server && token && !reason) api.logout(server, token).catch(() => {});
}

/** Forgets a server entirely (and its session). */
export async function forgetServer(server: string): Promise<void> {
  if (state.server === server && state.phase === 'ready') await signOut();
  const {[server]: _, ...rest} = sessions;
  sessions = rest;
  await saveSessions(sessions);
  const s = settingsStore.get();
  const {[server]: __, ...serverNames} = s.serverNames;
  await settingsStore.update({
    servers: s.servers.filter(x => x !== server),
    serverNames,
    activeServer: s.activeServer === server ? null : s.activeServer,
  });
  if (state.server === server) set({server: settingsStore.get().servers[0] ?? null});
}

export function hasSession(server: string): boolean {
  return !!sessions[server];
}

/** Runs an authenticated call; a rejected session signs out. */
async function authed<T>(fn: (server: string, token: string) => Promise<T>): Promise<T> {
  const {server, token} = state;
  if (!server || !token) throw new ApiError(401, 'Not signed in');
  try {
    return await fn(server, token);
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      await signOut('Your session ended (it expired, or the root password changed). Sign in again.');
    }
    throw e;
  }
}

let refreshing: Promise<void> | null = null;

/** Refetches everything for the open server. */
export function refresh(): Promise<void> {
  refreshing ??= (async () => {
    try {
      const [info, relays, deployments, bridges, domains, ipGroups, emailGroups, schema, status] = await authed((s, t) =>
        Promise.all([
          api.info(s, t),
          api.relays(s, t),
          api.deployments(s, t),
          api.bridges(s, t),
          api.domains(s, t),
          api.ipGroups(s, t),
          // A server from before email groups answers 404: treat it as none.
          api.emailGroups(s, t).catch((): EmailGroup[] => []),
          // Likewise a server from before schemas.
          api.schemaStatus(s, t).catch((): SchemaStatus | null => null),
          api.status(s, t),
        ]),
      );
      if (state.phase === 'ready')
        set({
          info,
          relays,
          deployments,
          bridges,
          domains,
          ipGroups,
          emailGroups,
          schema: stampOverride(schema),
          live: status.relays ?? {},
          publicLive: status.public ?? {},
          loaded: true,
          error: null,
        });
    } catch (e) {
      if (state.phase === 'ready') set({error: (e as Error).message, loaded: true});
      throw e;
    } finally {
      refreshing = null;
    }
  })();
  return refreshing;
}

// ---- mutations: each refetches, so every list stays the server's truth ----

export async function createRelay(name: string): Promise<{relay: Relay; token: string}> {
  const out = await authed((s, t) => api.createRelay(s, t, name));
  await refresh().catch(() => {});
  return out;
}

export async function renameRelay(id: string, name: string): Promise<void> {
  await authed((s, t) => api.renameRelay(s, t, id, name));
  await refresh().catch(() => {});
}

export async function setRelaySlug(id: string, slug: string): Promise<void> {
  await authed((s, t) => api.setRelaySlug(s, t, id, slug));
  await refresh().catch(() => {});
}

export async function setRelayManage(id: string, on: boolean): Promise<void> {
  await authed((s, t) => api.setRelayManage(s, t, id, on));
  await refresh().catch(() => {});
}

export async function deleteRelay(id: string): Promise<void> {
  await authed((s, t) => api.deleteRelay(s, t, id));
  await refresh().catch(() => {});
}

export async function rotateRelay(id: string): Promise<string> {
  const {token} = await authed((s, t) => api.rotateRelay(s, t, id));
  await refresh().catch(() => {});
  return token;
}

export async function saveDeployment(id: string | null, input: DeploymentInput): Promise<Deployment> {
  const d = await authed((s, t) => (id ? api.updateDeployment(s, t, id, input) : api.createDeployment(s, t, input)));
  await refresh().catch(() => {});
  return d;
}

export async function deleteDeployment(id: string): Promise<void> {
  await authed((s, t) => api.deleteDeployment(s, t, id));
  await refresh().catch(() => {});
}

/** expiresInS: seconds the token works for; 0 never expires. */
export async function createToken(id: string, name: string, expiresInS: number): Promise<string> {
  const {token} = await authed((s, t) => api.createToken(s, t, id, name, expiresInS));
  await refresh().catch(() => {});
  return token;
}

export async function deleteToken(id: string, tid: string): Promise<void> {
  await authed((s, t) => api.deleteToken(s, t, id, tid));
  await refresh().catch(() => {});
}

export async function createPathRule(id: string, input: PathRuleInput): Promise<PathRule> {
  const r = await authed((s, t) => api.createPathRule(s, t, id, input));
  await refresh().catch(() => {});
  return r;
}

export async function updatePathRule(id: string, rid: string, input: PathRuleInput): Promise<PathRule> {
  const r = await authed((s, t) => api.updatePathRule(s, t, id, rid, input));
  await refresh().catch(() => {});
  return r;
}

export async function deletePathRule(id: string, rid: string): Promise<void> {
  await authed((s, t) => api.deletePathRule(s, t, id, rid));
  await refresh().catch(() => {});
}

export async function saveBridge(id: string | null, input: BridgeInput): Promise<Bridge> {
  const b = await authed((s, t) => (id ? api.updateBridge(s, t, id, input) : api.createBridge(s, t, input)));
  await refresh().catch(() => {});
  return b;
}

export async function deleteBridge(id: string): Promise<void> {
  await authed((s, t) => api.deleteBridge(s, t, id));
  await refresh().catch(() => {});
}

export async function createDomain(name: string): Promise<Domain> {
  const d = await authed((s, t) => api.createDomain(s, t, name));
  await refresh().catch(() => {});
  return d;
}

export async function deleteDomain(id: string): Promise<void> {
  await authed((s, t) => api.deleteDomain(s, t, id));
  await refresh().catch(() => {});
}

export async function saveIPGroup(id: string | null, input: IPGroupInput): Promise<IPGroup> {
  const g = await authed((s, t) => (id ? api.updateIPGroup(s, t, id, input) : api.createIPGroup(s, t, input)));
  await refresh().catch(() => {});
  return g;
}

export async function deleteIPGroup(id: string): Promise<void> {
  await authed((s, t) => api.deleteIPGroup(s, t, id));
  await refresh().catch(() => {});
}

export function checkDomainDns(id: string): Promise<DomainDns> {
  return authed((s, t) => api.domainDns(s, t, id));
}

export function checkBridgeDns(id: string): Promise<DomainDns> {
  return authed((s, t) => api.bridgeDns(s, t, id));
}

// ---- live status ----

let ws: WebSocket | null = null;
let retry: ReturnType<typeof setTimeout> | null = null;
let backoff = 1000;
let refetchTimer: ReturnType<typeof setTimeout> | null = null;
let generation = 0;

function connect() {
  const {server, token} = state;
  if (!server || !token || typeof WebSocket === 'undefined') return;
  const gen = ++generation;
  const sock = new WebSocket(server.replace(/^http/, 'ws') + '/v1/ws');
  ws = sock;
  sock.onopen = () => sock.send(JSON.stringify({type: 'auth', token}));
  sock.onmessage = e => {
    if (gen !== generation) return;
    let msg: any;
    try {
      msg = JSON.parse(String(e.data));
    } catch {
      return;
    }
    if (msg.type === 'status') {
      backoff = 1000;
      set({live: msg.relays ?? {}, publicLive: msg.public ?? {}, connected: true});
    } else if (msg.type === 'changed') {
      // Several changes tend to land together; refetch once.
      if (refetchTimer) clearTimeout(refetchTimer);
      refetchTimer = setTimeout(() => refresh().catch(() => {}), 150);
    }
  };
  sock.onclose = e => {
    if (gen !== generation) return;
    ws = null;
    set({connected: false});
    if (e.code === 4001) {
      signOut('Your session ended (it expired, or the root password changed). Sign in again.');
      return;
    }
    retry = setTimeout(() => {
      // Back after a gap: whatever changed meanwhile wasn't pushed.
      refresh().catch(() => {});
      connect();
    }, backoff);
    backoff = Math.min(backoff * 2, 60_000);
  };
}

function disconnect() {
  generation++;
  if (retry) clearTimeout(retry);
  if (refetchTimer) clearTimeout(refetchTimer);
  retry = refetchTimer = null;
  backoff = 1000;
  ws?.close();
  ws = null;
}

/** Reconnects at once (the app came back to the foreground). */
export function wake(): void {
  if (state.phase !== 'ready') return;
  refresh().catch(() => {});
  if (!ws) {
    if (retry) clearTimeout(retry);
    retry = null;
    backoff = 1000;
    connect();
  }
}

// ---- derived ----

export type Health = 'ok' | 'offline' | 'unreachable' | 'unknown' | 'disabled';

/** How a deployment is doing right now, from its relay's live state. */
export function deploymentHealth(d: Deployment, live: Record<string, RelayState>): {health: Health; detail: string} {
  if (!d.enabled) return {health: 'disabled', detail: 'Turned off'};
  const r = live[d.relayId];
  if (!r) return {health: 'offline', detail: 'Relay offline'};
  const h = r.health?.[d.id];
  if (h === undefined) return {health: 'unknown', detail: 'Checking…'};
  if (h === 'ok') return {health: 'ok', detail: 'Reachable'};
  return {health: 'unreachable', detail: h};
}

/** Whether a relay can serve bridges of this protocol (from what it said when it last connected). */
export function supportsBridges(r: Relay | undefined, protocol: BridgeProtocol): boolean {
  return !!r?.features?.includes(`bridge-${protocol}`);
}

/**
 * A bridge works when its listening side is up (the relay, or the server for
 * a public one) and the relay at the far end is online and can serve it.
 */
export function bridgeHealth(
  b: Bridge,
  relays: Relay[],
  live: Record<string, RelayState>,
  publicLive: Record<string, string>,
): {health: Health; detail: string} {
  if (!b.enabled) return {health: 'disabled', detail: 'Turned off'};
  const ends = b.public ? [b.toRelayId] : [b.fromRelayId, b.toRelayId];
  for (const id of ends) {
    const r = relays.find(x => x.id === id);
    const name = r?.name ?? 'a relay';
    const l = live[id];
    if (!l) return {health: 'offline', detail: `${name} is offline`};
    if (!(l.features ?? r?.features ?? []).includes(`bridge-${b.protocol}`))
      return {health: 'offline', detail: `${name} needs updating for ${b.protocol.toUpperCase()} bridges`};
  }
  const listen = b.public ? publicLive[b.id] ?? (b.listenError || undefined) : live[b.fromRelayId]?.health?.[b.id];
  const reach = live[b.toRelayId]?.health?.[b.id];
  if (listen === undefined || reach === undefined) return {health: 'unknown', detail: 'Checking…'};
  if (listen !== 'ok') return {health: 'unreachable', detail: listen};
  if (reach !== 'ok') return {health: 'unreachable', detail: reach};
  return {health: 'ok', detail: b.public ? 'Open to the internet' : 'Listening, target reachable'};
}

/** Tests can start from a clean slate. */
export function resetStore(): void {
  disconnect();
  sessions = {};
  state = initial;
  emit();
}

export async function saveEmailGroup(id: string | null, input: EmailGroupInput): Promise<EmailGroup> {
  const g = await authed((s, t) => (id ? api.updateEmailGroup(s, t, id, input) : api.createEmailGroup(s, t, input)));
  await refresh().catch(() => {});
  return g;
}

export async function deleteEmailGroup(id: string): Promise<void> {
  await authed((s, t) => api.deleteEmailGroup(s, t, id));
  await refresh().catch(() => {});
}

/** Whether the open server says it supports feature (see ServerInfo.features). */
export function supports(info: ServerInfo | null | undefined, feature: string): boolean {
  return !!info?.features?.includes(feature);
}

// ---- API tokens, override mode, the schema ----

export function listApiTokens(): Promise<ApiToken[]> {
  return authed((s, t) => api.apiTokens(s, t));
}

/** Makes a schema-scoped API token; the token itself is only shown now. */
export function createApiToken(name: string): Promise<{apiToken: ApiToken; token: string}> {
  return authed((s, t) => api.createApiToken(s, t, name));
}

export function deleteApiToken(id: string): Promise<void> {
  return authed((s, t) => api.deleteApiToken(s, t, id));
}

/** Turns override mode on (for minutes, default an hour) or off. */
export async function setOverride(on: boolean, minutes?: number): Promise<void> {
  await authed((s, t) => api.setOverride(s, t, on, minutes));
  await refresh().catch(() => {});
}

export function planSchema(doc: string): Promise<SchemaPlan> {
  return authed((s, t) => api.schemaPlan(s, t, doc));
}

/** Applies doc if its plan is still planHash (else a 409 with the new plan). */
export async function applySchema(doc: string, planHash: string): Promise<{applied: boolean; revision: number}> {
  const out = await authed((s, t) => api.schemaApply(s, t, doc, planHash));
  await refresh().catch(() => {});
  return out;
}

export function exportSchema(applied: boolean): Promise<string> {
  return authed((s, t) => api.schemaExport(s, t, applied));
}

/** Whether override mode is on right now (it ends by itself at `until`). */
/**
 * When override mode ends, by this device's clock (null: it's off). The
 * server's secondsLeft, counted from when the status came, is right even
 * when the device clock isn't.
 */
export function overrideEndsAt(schema: SchemaStatus | null): number | null {
  const o = schema?.override;
  if (!o?.on || !o.until) return null;
  return o.endsAt ?? Date.parse(o.until);
}

export function overrideOn(schema: SchemaStatus | null, now = Date.now()): boolean {
  const end = overrideEndsAt(schema);
  return end !== null && end > now;
}

/** Stamps when override ends by this device's clock, as it arrives. */
function stampOverride(schema: SchemaStatus | null): SchemaStatus | null {
  const left = schema?.override.secondsLeft;
  if (!schema || !schema.override.on || typeof left !== 'number') return schema;
  return {...schema, override: {...schema.override, endsAt: Date.now() + left * 1000}};
}

/**
 * Whether the app may change field (its name in the schema document, like
 * "target_port") of a resource with managed: not when the schema sets it,
 * unless override mode is on.
 */
export function locked(managed: {fields: string[]} | null | undefined, field: string, schema: SchemaStatus | null): boolean {
  return !!managed?.fields.includes(field) && !overrideOn(schema);
}

/** The schema's own entries of a list field, which the app can't remove (empty in override mode). */
export function lockedEntries(managed: {entries: Record<string, string[]>} | null | undefined, field: string, schema: SchemaStatus | null): string[] {
  return overrideOn(schema) ? [] : managed?.entries[field] ?? [];
}

/** Whether the schema lists collection in full, so nothing can be added to it here. */
export function strictCollection(schema: SchemaStatus | null, collection: string): boolean {
  return !!schema?.strict.includes(collection) && !overrideOn(schema);
}
