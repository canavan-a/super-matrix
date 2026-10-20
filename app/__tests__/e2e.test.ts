/**
 * @jest-environment node
 */
// End to end against real binaries: the app's store signs in to a freshly
// built supermatrix-server (TLS off), adds a relay, runs a real
// supermatrix-relay with its token, and exposes a local HTTP service through
// it under each auth mode. Runs only with E2E=1, since it builds the Go code:
//
//   E2E=1 npx jest __tests__/e2e.test.ts
import {ChildProcess, execFileSync, spawn} from 'child_process';
import fs from 'fs';
import http from 'http';
import {AddressInfo} from 'net';
import os from 'os';
import path from 'path';

const run = process.env.E2E === '1' ? describe : describe.skip;

const ROOT_PW = 'e2e root password';

async function until<T>(fn: () => T | undefined | null | false | Promise<T | undefined | null | false>, ms = 10_000): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timed out waiting');
    await new Promise(r => setTimeout(r, 50));
  }
}

/** A GET through the server with a chosen Host header (fetch can't set Host). */
function get(port: number, host: string, p: string, headers: Record<string, string> = {}): Promise<{status: number; body: string}> {
  return new Promise((resolve, reject) => {
    const req = http.request({host: '127.0.0.1', port, path: p, headers: {...headers, Host: host}}, res => {
      let body = '';
      res.on('data', c => (body += c));
      res.on('end', () => resolve({status: res.statusCode ?? 0, body}));
    });
    req.on('error', reject);
    req.end();
  });
}

run('end to end', () => {
  let server: ChildProcess;
  let relay: ChildProcess | undefined;
  let target: http.Server;
  let port: number;
  let targetPort: number;
  let url: string;
  let dir: string;
  let relayBin: string;
  let store: typeof import('../src/store');
  let forms: typeof import('../src/screens/DeploymentScreen');

  beforeAll(async () => {
    dir = fs.mkdtempSync(path.join(os.tmpdir(), 'supermatrix-e2e-'));
    const serverBin = path.join(dir, 'supermatrix-server');
    relayBin = path.join(dir, 'supermatrix-relay');
    const cwd = path.resolve(__dirname, '../../server');
    const env = {...process.env, CGO_ENABLED: '0'};
    execFileSync('go', ['build', '-o', serverBin, './cmd/supermatrix-server'], {
      cwd,
      env,
    });
    execFileSync('go', ['build', '-o', relayBin, './cmd/supermatrix-relay'], {
      cwd,
      env,
    });

    target = http.createServer((req, res) => {
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({path: req.url, auth: req.headers.authorization ?? ''}));
    });
    await new Promise<void>(r => target.listen(0, '127.0.0.1', r));
    targetPort = (target.address() as AddressInfo).port;

    port = 20000 + Math.floor(Math.random() * 20000);
    url = `http://127.0.0.1:${port}`;
    server = spawn(serverBin, [], {
      env: {
        ...process.env,
        SUPERMATRIX_ADMIN_HOST: '127.0.0.1',
        SUPERMATRIX_BASE_DOMAIN: 'e2e.test',
        SUPERMATRIX_TLS: 'off',
        SUPERMATRIX_HTTP_ADDR: `127.0.0.1:${port}`,
        SUPERMATRIX_DATA_DIR: dir,
        SUPERMATRIX_ROOT_PASSWORD: ROOT_PW,
      },
      stdio: 'ignore',
    });
    await until(async () => {
      try {
        return (await fetch(url + '/health')).ok;
      } catch {
        return false;
      }
    });

    jest.isolateModules(() => {
      store = require('../src/store');
      forms = require('../src/screens/DeploymentScreen');
    });
    await store.init();
  }, 180_000);

  afterAll(() => {
    store?.resetStore();
    relay?.kill();
    server?.kill();
    target?.close();
    fs.rmSync(dir, {recursive: true, force: true});
  });

  it('rejects a wrong root password and an address that is not a server', async () => {
    expect(store.getState().phase).toBe('signedOut');
    await expect(store.signIn(url, 'wrong')).rejects.toThrow('wrong password');
    await expect(store.signIn('http://127.0.0.1:1', ROOT_PW)).rejects.toThrow('isn’t answering as a Super Matrix server');
    expect(store.getState().phase).toBe('signedOut');
  });

  it('signs in and loads the server', async () => {
    await store.signIn(url, ROOT_PW);
    expect(store.getState().phase).toBe('ready');
    await until(() => store.getState().loaded);
    expect(store.getState().info).toMatchObject({
      adminHost: '127.0.0.1',
      tls: false,
    });
    // SUPERMATRIX_BASE_DOMAIN seeds the first domain.
    expect(store.getState().domains.map(d => d.label)).toEqual(['*.e2e.test']);
    // The live socket comes up.
    await until(() => store.getState().connected);
  });

  it('adds a relay that connects with its token', async () => {
    const {relay: r, token} = await store.createRelay('e2e laptop');
    expect(token).toMatch(/^smr_/);
    relay = spawn(relayBin, [], {
      env: {
        ...process.env,
        SUPERMATRIX_SERVER_URL: url,
        SUPERMATRIX_RELAY_TOKEN: token,
      },
      stdio: 'ignore',
    });
    // Pushed over the WebSocket, not polled.
    await until(() => store.getState().live[r.id]);
    await until(() => store.getState().relays.find(x => x.id === r.id)?.online);
    // The dev server offers QUIC on the HTTP port (UDP, self-signed and
    // pinned), and the relay takes it.
    expect(store.getState().info?.quic?.addr).toBe(`127.0.0.1:${port}`);
    expect(store.getState().live[r.id].transport).toBe('quic');
  });

  it('exposes a service behind each auth mode', async () => {
    const relayId = store.getState().relays[0].id;
    const form = {
      ...forms.formFrom(null, relayId, store.getState().domains[0].id),
      subdomain: 'svc',
      targetHost: '127.0.0.1',
      targetPort: String(targetPort),
      authMode: 'basic' as const,
      basicUser: 'ann',
      basicPassword: 'open sesame',
    };
    const d = await store.saveDeployment(null, forms.formToInput(form));
    expect(d.host).toBe('svc.e2e.test');
    expect(store.getState().deployments.map(x => x.subdomain)).toEqual(['svc']);

    // The relay probes its new target and reports it healthy.
    await until(() => store.deploymentHealth(store.getState().deployments[0], store.getState().live).health === 'ok');

    const basic = 'Basic ' + Buffer.from('ann:open sesame').toString('base64');
    expect((await get(port, 'svc.e2e.test', '/x')).status).toBe(401);
    const ok = await until(async () => {
      const res = await get(port, 'svc.e2e.test', '/x', {Authorization: basic});
      return res.status === 200 && res;
    });
    expect(JSON.parse(ok.body)).toEqual({path: '/x', auth: ''});

    // Tokens: only after switching to token mode do they get in.
    await store.saveDeployment(d.id, {authMode: 'token'});
    const tok = await store.createToken(d.id, 'ci');
    expect((await get(port, 'svc.e2e.test', '/', {Authorization: `Bearer ${tok}`})).status).toBe(200);
    expect((await get(port, 'svc.e2e.test', '/', {Authorization: 'Bearer smt_nope'})).status).toBe(401);
    expect(store.getState().deployments[0].tokens.map(t => t.name)).toEqual(['ci']);

    // Login mode sends browsers to the sign-in page.
    await store.saveDeployment(d.id, {authMode: 'login'});
    const login = await get(port, 'svc.e2e.test', '/page', {
      Accept: 'text/html',
    });
    expect(login.status).toBe(303);

    // Public.
    await store.saveDeployment(d.id, {authMode: 'none'});
    expect((await get(port, 'svc.e2e.test', '/')).status).toBe(200);

    // Server-side validation comes back as a readable error.
    await expect(
      store.saveDeployment(null, {
        ...forms.formToInput(form),
        subdomain: 'svc',
      }),
    ).rejects.toThrow('svc.e2e.test is taken');
  });

  it('shows a relay going offline', async () => {
    relay?.kill();
    relay = undefined;
    await until(() => Object.keys(store.getState().live).length === 0);
    expect(store.deploymentHealth(store.getState().deployments[0], store.getState().live).health).toBe('offline');
  });

  it('signs out', async () => {
    await store.signOut();
    expect(store.getState().phase).toBe('signedOut');
    expect(store.hasSession(url)).toBe(false);
  });
});
