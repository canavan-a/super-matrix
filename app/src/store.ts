// The app's state: which server is open, its session, and what's on it
// (relays, deployments, live relay status). Lists come over HTTP; a
// WebSocket to /v1/ws pushes live relay status and says when to refetch.
// Screens read it through useStore(); every change calls emit().
import {useEffect, useReducer, useRef} from 'react';

import {api, ApiError, Deployment, DeploymentInput, Domain, DomainDns, Relay, RelayState, ServerInfo} from './api';
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
  domains: Domain[];
  /** relay id → live state; a relay missing here is offline */
  live: Record<string, RelayState>;
  /** the lists have been fetched at least once for this server */
  loaded: boolean;
  /** the live WebSocket is up */
  connected: boolean;
  /** why the last sign-in or refresh failed, for the screen to show */
  error: string | null;
  /** a sign-in that ended (expired, password changed), shown on the sign-in screen */
  signedOutReason: string | null;
}

const initial: State = {
  phase: 'loading',
  server: null,
  token: null,
  info: null,
  relays: [],
  deployments: [],
  domains: [],
  live: {},
  loaded: false,
  connected: false,
  error: null,
  signedOutReason: null,
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
  disconnect();
  set({...initial, phase: 'signedOut', server: null});
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
  await settingsStore.update({
    servers: s.servers.filter(x => x !== server),
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
      const [info, relays, deployments, domains, live] = await authed((s, t) =>
        Promise.all([api.info(s, t), api.relays(s, t), api.deployments(s, t), api.domains(s, t), api.status(s, t)]),
      );
      if (state.phase === 'ready') set({info, relays, deployments, domains, live, loaded: true, error: null});
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

export async function createToken(id: string, name: string): Promise<string> {
  const {token} = await authed((s, t) => api.createToken(s, t, id, name));
  await refresh().catch(() => {});
  return token;
}

export async function deleteToken(id: string, tid: string): Promise<void> {
  await authed((s, t) => api.deleteToken(s, t, id, tid));
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

export function checkDomainDns(id: string): Promise<DomainDns> {
  return authed((s, t) => api.domainDns(s, t, id));
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
      set({live: msg.relays ?? {}, connected: true});
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

/** Tests can start from a clean slate. */
export function resetStore(): void {
  disconnect();
  sessions = {};
  state = initial;
  emit();
}
