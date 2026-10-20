// The Super Matrix server's admin API (server/api). Every call but login and
// health carries the session token the root password was traded for.

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
  }
}

/** How long an API call may take before it's given up on. */
const REQUEST_TIMEOUT_MS = 15_000;

export async function request<T>(server: string, method: string, path: string, token: string | null, body?: unknown): Promise<T> {
  // A request that never answers (a dead connection, an unreachable server)
  // fails after a while instead of holding everything waiting on it forever.
  const abort = new AbortController();
  const timer = setTimeout(() => abort.abort(), REQUEST_TIMEOUT_MS);
  let res: Response;
  let text: string;
  try {
    res = await fetch(server + path, {
      method,
      headers: {
        ...(token ? {Authorization: `Bearer ${token}`} : {}),
        ...(body !== undefined ? {'Content-Type': 'application/json'} : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: abort.signal,
    });
    text = await res.text();
  } catch (e) {
    if (abort.signal.aborted) throw new ApiError(0, `${server.replace(/^https?:\/\//, '')} didn't answer`);
    throw new ApiError(0, `Can't reach ${server.replace(/^https?:\/\//, '')}`);
  } finally {
    clearTimeout(timer);
  }
  let json: any = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    // not JSON
  }
  if (!res.ok) {
    throw new ApiError(res.status, json?.error ?? `HTTP ${res.status}`);
  }
  return json as T;
}

// ---- types (server/api/*.go DTOs) ----

export type AuthMode = 'none' | 'login' | 'basic' | 'token';
export type CertStatus = 'pending' | 'ok' | 'error' | 'off';

export interface ServerInfo {
  adminHost: string;
  tls: boolean;
  version: string;
  serverUrl: string;
  /** where relays reach QUIC; null when the server has it off */
  quic: {addr: string; serverName: string; pin?: string} | null;
}

export type Transport = 'quic' | 'websocket';

export interface Relay {
  id: string;
  name: string;
  createdAt: string;
  lastSeenAt: string | null;
  lastIp: string;
  version: string;
  hostname: string;
  online: boolean;
  deployments: number;
  /** how it last connected, and (on the WebSocket) why not QUIC */
  transport: Transport | '';
  fallback: string;
}

/** A name deployments live on: "*.example.com" (each gets a subdomain) or an exact host like "status.foo.org". */
export interface Domain {
  id: string;
  /** without the "*." */
  name: string;
  wildcard: boolean;
  /** "*.example.com" or "status.foo.org" */
  label: string;
  deployments: number;
  createdAt: string;
}

/** Whether a domain resolves to this server (compared with the admin host). */
export interface DomainDns {
  ok: boolean;
  /** the name that was looked up (a random one under a wildcard) */
  probe: string;
  addresses: string[];
  expected: string[];
  detail: string;
}

export interface DeploymentToken {
  id: string;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface Deployment {
  id: string;
  relayId: string;
  domainId: string;
  subdomain: string;
  host: string;
  url: string;
  protocol: 'http' | 'https';
  targetHost: string;
  targetPort: number;
  tlsSkipVerify: boolean;
  sni: string;
  preserveHost: boolean;
  enabled: boolean;
  authMode: AuthMode;
  hasLoginPassword: boolean;
  allowRootLogin: boolean;
  sessionTtlS: number;
  basicUser: string;
  hasBasicPassword: boolean;
  ipAllowlist: string[];
  certStatus: CertStatus;
  certExpiresAt: string | null;
  certError: string;
  tokens: DeploymentToken[];
  createdAt: string;
  updatedAt: string;
}

/** A create or update. Passwords are only sent when set ("" clears one). */
export interface DeploymentInput {
  relayId?: string;
  domainId?: string;
  subdomain?: string;
  protocol?: 'http' | 'https';
  targetHost?: string;
  targetPort?: number;
  tlsSkipVerify?: boolean;
  sni?: string;
  preserveHost?: boolean;
  enabled?: boolean;
  authMode?: AuthMode;
  loginPassword?: string;
  allowRootLogin?: boolean;
  sessionTtlS?: number;
  basicUser?: string;
  basicPassword?: string;
  ipAllowlist?: string[];
}

/** A connected relay's live state (/v1/status, and the WebSocket). */
export interface RelayState {
  online: boolean;
  connectedAt: string;
  remote: string;
  version: string;
  hostname: string;
  /** deployment id → "ok" or why the relay can't reach its target */
  health: Record<string, string>;
  transport: Transport;
  /** why it isn't on QUIC, when it's on the WebSocket */
  fallback?: string;
  /** smoothed round-trip time; 0 until measured */
  rttMs: number;
  bytesSent: number;
  bytesReceived: number;
  /** QUIC only */
  packetsLost: number;
  /** upstream connections open through it now (pooled ones included) */
  streams: number;
}

// ---- endpoints ----

export const api = {
  health: async (server: string): Promise<boolean> => {
    try {
      const res = await fetch(server + '/health');
      const j = await res.json();
      return res.ok && j?.app === 'supermatrix';
    } catch {
      return false;
    }
  },
  login: (server: string, password: string) => request<{token: string; expiresAt: string}>(server, 'POST', '/v1/login', null, {password}),
  logout: (server: string, token: string) => request<void>(server, 'POST', '/v1/logout', token),
  info: (server: string, token: string) => request<ServerInfo>(server, 'GET', '/v1/info', token),
  status: (server: string, token: string) =>
    request<{relays: Record<string, RelayState>}>(server, 'GET', '/v1/status', token).then(r => r.relays),

  relays: (server: string, token: string) => request<Relay[]>(server, 'GET', '/v1/relays', token),
  createRelay: (server: string, token: string, name: string) =>
    request<{relay: Relay; token: string}>(server, 'POST', '/v1/relays', token, {name}),
  renameRelay: (server: string, token: string, id: string, name: string) =>
    request<Relay>(server, 'PATCH', `/v1/relays/${id}`, token, {name}),
  deleteRelay: (server: string, token: string, id: string) => request<void>(server, 'DELETE', `/v1/relays/${id}`, token),
  rotateRelay: (server: string, token: string, id: string) => request<{token: string}>(server, 'POST', `/v1/relays/${id}/rotate`, token),

  domains: (server: string, token: string) => request<Domain[]>(server, 'GET', '/v1/domains', token),
  createDomain: (server: string, token: string, name: string) => request<Domain>(server, 'POST', '/v1/domains', token, {name}),
  deleteDomain: (server: string, token: string, id: string) => request<void>(server, 'DELETE', `/v1/domains/${id}`, token),
  domainDns: (server: string, token: string, id: string) => request<DomainDns>(server, 'GET', `/v1/domains/${id}/dns`, token),

  deployments: (server: string, token: string) => request<Deployment[]>(server, 'GET', '/v1/deployments', token),
  createDeployment: (server: string, token: string, d: DeploymentInput) => request<Deployment>(server, 'POST', '/v1/deployments', token, d),
  updateDeployment: (server: string, token: string, id: string, d: DeploymentInput) =>
    request<Deployment>(server, 'PATCH', `/v1/deployments/${id}`, token, d),
  deleteDeployment: (server: string, token: string, id: string) => request<void>(server, 'DELETE', `/v1/deployments/${id}`, token),
  createToken: (server: string, token: string, id: string, name: string) =>
    request<{id: string; name: string; token: string}>(server, 'POST', `/v1/deployments/${id}/tokens`, token, {name}),
  deleteToken: (server: string, token: string, id: string, tid: string) =>
    request<void>(server, 'DELETE', `/v1/deployments/${id}/tokens/${tid}`, token),
};
