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

export type AuthMode = 'none' | 'login' | 'otp' | 'basic' | 'token';
export type CertStatus = 'pending' | 'ok' | 'error' | 'off';
/** What the target speaks: http(s) is proxied, ssh is reached with the supermatrix CLI or the browser shell. */
export type Protocol = 'http' | 'https' | 'ssh';

export interface ServerInfo {
  adminHost: string;
  tls: boolean;
  version: string;
  serverUrl: string;
  /** where relays reach QUIC; null when the server has it off */
  quic: {addr: string; serverName: string; pin?: string} | null;
  /** whether email-code deployments can send codes (SMTP configured) */
  smtp: boolean;
  /** ports public bridges may use, e.g. "20000-20100,3478"; "" when public bridges are off */
  publicPorts: string;
}

export type Transport = 'quic' | 'websocket';

export interface Relay {
  id: string;
  name: string;
  /** friendly ID config files name it by; missing from servers before slugs */
  slug?: string;
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
  /** whether the relay can manage its own deployments (CLI on its machine) */
  manageDeployments: boolean;
  /** what it said it supports when it last connected, e.g. "bridge-tcp" */
  features: string[];
  /** bridges that listen on it or lead to it */
  bridges: number;
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
  /** public bridges named on it */
  bridges: number;
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
  /** null never expires */
  expiresAt: string | null;
}

export interface PathRule {
  id: string;
  /** "/secret" matches "/secret" and "/secret/x", not "/secretly" */
  pathPrefix: string;
  ipAllowlist: string[];
  createdAt: string;
  updatedAt: string;
}

/** A create or update; missing fields stay as they are on update. */
export interface PathRuleInput {
  pathPrefix?: string;
  ipAllowlist?: string[];
}

export interface Deployment {
  id: string;
  relayId: string;
  domainId: string;
  subdomain: string;
  host: string;
  url: string;
  protocol: Protocol;
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
  /** who can get an email code (otp mode) */
  otpEmails: string[];
  ipAllowlist: string[];
  /** IDs of the IP groups the allow list includes */
  ipGroups: string[];
  /** ssh only: the CLI proxy and the browser shell, the username the shell suggests */
  sshCli: boolean;
  sshShell: boolean;
  sshUser: string;
  /** the target's host key (SHA256:…), saved the first time the shell connects; "" until then */
  sshHostKey: string;
  /** caps requests from a single client address */
  rateLimitEnabled: boolean;
  rateLimitRequests: number;
  /** the window's length in seconds: 60 = per minute, 1 = per second */
  rateLimitPeriodS: number;
  /** extra requests allowed in a short 1s burst on top of the sustained rate; 0 = none */
  rateLimitBurst: number;
  certStatus: CertStatus;
  certExpiresAt: string | null;
  certError: string;
  tokens: DeploymentToken[];
  pathRules: PathRule[];
  createdAt: string;
  updatedAt: string;
}

/** A create or update. Passwords are only sent when set ("" clears one). */
export interface DeploymentInput {
  relayId?: string;
  domainId?: string;
  subdomain?: string;
  protocol?: Protocol;
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
  otpEmails?: string[];
  ipAllowlist?: string[];
  /** replaces the IP groups the allow list includes, by ID */
  ipGroups?: string[];
  sshCli?: boolean;
  sshShell?: boolean;
  sshUser?: string;
  /** only "": forget the saved host key so the next one is trusted */
  sshHostKey?: '';
  rateLimitEnabled?: boolean;
  rateLimitRequests?: number;
  rateLimitPeriodS?: number;
  rateLimitBurst?: number;
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
  /** what it supports beyond deployments, e.g. "bridge-tcp" */
  features?: string[];
}

export type BridgeProtocol = 'tcp' | 'udp';

/**
 * A TCP or UDP port that leads to targetHost:targetPort on toRelay's network.
 * It listens on fromRelay's machine, or (public) on the server itself.
 */
export interface Bridge {
  id: string;
  name: string;
  /** friendly ID config files name it by; missing from servers before slugs */
  slug?: string;
  protocol: BridgeProtocol;
  enabled: boolean;
  public: boolean;
  /** host:port people reach a public bridge on; "" otherwise */
  publicAddress: string;
  /** "" for a public bridge */
  fromRelayId: string;
  listenHost: string;
  listenPort: number;
  toRelayId: string;
  targetHost: string;
  targetPort: number;
  /** how long a quiet UDP sender keeps its flow */
  udpIdleS: number;
  /** public bridges only */
  ipAllowlist: string[];
  /** public bridges only: IDs of the IP groups the allow list includes */
  ipGroups: string[];
  /** public bridges: the domain and label of its DNS name, and the name itself ("" for none) */
  domainId: string;
  subdomain: string;
  host: string;
  /** why a public bridge's port couldn't be bound; "" when it's fine */
  listenError: string;
  createdAt: string;
  updatedAt: string;
}

export interface BridgeInput {
  name?: string;
  /** left out to keep the current one (or, on create, have one made from the name) */
  slug?: string;
  protocol?: BridgeProtocol;
  enabled?: boolean;
  public?: boolean;
  fromRelayId?: string;
  listenHost?: string;
  listenPort?: number;
  toRelayId?: string;
  targetHost?: string;
  targetPort?: number;
  udpIdleS?: number;
  ipAllowlist?: string[];
  ipGroups?: string[];
  /** public bridges: "" for no DNS name */
  domainId?: string;
  subdomain?: string;
}

/** A named set of addresses and CIDR ranges that allow lists include by reference. */
export interface IPGroup {
  id: string;
  name: string;
  /** normalized CIDRs (a bare address comes back as /32 or /128) */
  entries: string[];
  /** how many deployments and public bridges include it */
  deployments: number;
  bridges: number;
  createdAt: string;
  updatedAt: string;
}

export interface IPGroupInput {
  name?: string;
  entries?: string[];
}

/** /v1/status: each online relay's live state, and each public bridge's listen status on the server. */
export interface LiveStatus {
  relays: Record<string, RelayState>;
  public?: Record<string, string>;
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
  status: (server: string, token: string) => request<LiveStatus>(server, 'GET', '/v1/status', token),

  relays: (server: string, token: string) => request<Relay[]>(server, 'GET', '/v1/relays', token),
  createRelay: (server: string, token: string, name: string) =>
    request<{relay: Relay; token: string}>(server, 'POST', '/v1/relays', token, {name}),
  renameRelay: (server: string, token: string, id: string, name: string) =>
    request<Relay>(server, 'PATCH', `/v1/relays/${id}`, token, {name}),
  setRelaySlug: (server: string, token: string, id: string, slug: string) =>
    request<Relay>(server, 'PATCH', `/v1/relays/${id}`, token, {slug}),
  setRelayManage: (server: string, token: string, id: string, manageDeployments: boolean) =>
    request<Relay>(server, 'PATCH', `/v1/relays/${id}`, token, {manageDeployments}),
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
  createToken: (server: string, token: string, id: string, name: string, expiresInS: number) =>
    request<{id: string; name: string; token: string; expiresAt: string | null}>(server, 'POST', `/v1/deployments/${id}/tokens`, token, {
      name,
      expiresInS,
    }),
  deleteToken: (server: string, token: string, id: string, tid: string) =>
    request<void>(server, 'DELETE', `/v1/deployments/${id}/tokens/${tid}`, token),
  createPathRule: (server: string, token: string, id: string, r: PathRuleInput) =>
    request<PathRule>(server, 'POST', `/v1/deployments/${id}/path-rules`, token, r),
  updatePathRule: (server: string, token: string, id: string, rid: string, r: PathRuleInput) =>
    request<PathRule>(server, 'PATCH', `/v1/deployments/${id}/path-rules/${rid}`, token, r),
  deletePathRule: (server: string, token: string, id: string, rid: string) =>
    request<void>(server, 'DELETE', `/v1/deployments/${id}/path-rules/${rid}`, token),

  ipGroups: (server: string, token: string) => request<IPGroup[]>(server, 'GET', '/v1/ip-groups', token),
  createIPGroup: (server: string, token: string, g: IPGroupInput) => request<IPGroup>(server, 'POST', '/v1/ip-groups', token, g),
  updateIPGroup: (server: string, token: string, id: string, g: IPGroupInput) =>
    request<IPGroup>(server, 'PATCH', `/v1/ip-groups/${id}`, token, g),
  deleteIPGroup: (server: string, token: string, id: string) => request<void>(server, 'DELETE', `/v1/ip-groups/${id}`, token),

  bridges: (server: string, token: string) => request<Bridge[]>(server, 'GET', '/v1/bridges', token),
  createBridge: (server: string, token: string, b: BridgeInput) => request<Bridge>(server, 'POST', '/v1/bridges', token, b),
  updateBridge: (server: string, token: string, id: string, b: BridgeInput) =>
    request<Bridge>(server, 'PATCH', `/v1/bridges/${id}`, token, b),
  deleteBridge: (server: string, token: string, id: string) => request<void>(server, 'DELETE', `/v1/bridges/${id}`, token),
  bridgeDns: (server: string, token: string, id: string) => request<DomainDns>(server, 'GET', `/v1/bridges/${id}/dns`, token),
};
