import {Deployment} from '../src/api';
import {formFrom, formToInput} from '../src/screens/DeploymentScreen';
import {relayInstall} from '../src/screens/RelaysScreen';
import {deploymentHealth} from '../src/store';

const existing: Deployment = {
  id: 'd1',
  relayId: 'r1',
  domainId: 'dom1',
  subdomain: 'grafana',
  host: 'grafana.example.com',
  url: 'https://grafana.example.com',
  protocol: 'https',
  targetHost: '10.0.0.5',
  targetPort: 3000,
  tlsSkipVerify: true,
  sni: 'grafana.lan',
  preserveHost: false,
  enabled: true,
  authMode: 'login',
  hasLoginPassword: true,
  allowRootLogin: true,
  sessionTtlS: 86400,
  basicUser: '',
  hasBasicPassword: false,
  ipAllowlist: ['10.0.0.0/8', '203.0.113.7/32'],
  certStatus: 'ok',
  certExpiresAt: '2027-01-01T00:00:00Z',
  certError: '',
  tokens: [],
  createdAt: '',
  updatedAt: '',
};

describe('deployment form', () => {
  it('starts a new deployment with safe defaults', () => {
    const f = formFrom(null, 'r9', 'dom9');
    expect(f).toMatchObject({
      relayId: 'r9',
      domainId: 'dom9',
      protocol: 'http',
      targetHost: '127.0.0.1',
      authMode: 'login',
      allowRootLogin: true,
      enabled: true,
    });
  });

  it('round-trips an existing deployment without sending passwords', () => {
    const input = formToInput(formFrom(existing));
    expect(input).toEqual({
      relayId: 'r1',
      domainId: 'dom1',
      subdomain: 'grafana',
      protocol: 'https',
      targetHost: '10.0.0.5',
      targetPort: 3000,
      tlsSkipVerify: true,
      sni: 'grafana.lan',
      preserveHost: false,
      enabled: true,
      authMode: 'login',
      allowRootLogin: true,
      sessionTtlS: 86400,
      basicUser: '',
      ipAllowlist: ['10.0.0.0/8', '203.0.113.7/32'],
    });
    expect('loginPassword' in input).toBe(false);
    expect('basicPassword' in input).toBe(false);
  });

  it('sends a new password, or "" to remove one', () => {
    expect(formToInput({...formFrom(existing), loginPassword: 'new one!'}).loginPassword).toBe('new one!');
    expect(formToInput({...formFrom(existing), clearLoginPassword: true}).loginPassword).toBe('');
    expect(formToInput({...formFrom(existing), authMode: 'basic', basicUser: ' ann ', basicPassword: 'pw123456'})).toMatchObject({
      basicUser: 'ann',
      basicPassword: 'pw123456',
    });
  });

  it('drops TLS options for plain http and tidies the address and allow list', () => {
    const input = formToInput({
      ...formFrom(existing),
      protocol: 'http',
      subdomain: ' Grafana ',
      ipAllowlist: ' 1.2.3.4 \n\n10.0.0.0/8,  ::1 ',
    });
    expect(input).toMatchObject({tlsSkipVerify: false, sni: '', subdomain: 'grafana', ipAllowlist: ['1.2.3.4', '10.0.0.0/8', '::1']});
  });
});

describe('relay install snippets', () => {
  it('carry the server URL and the token for every way of installing', () => {
    const all = relayInstall('https://matrix.example.com', 'smr_secret');
    for (const kind of ['nixos', 'systemd', 'run'] as const) {
      const text = all[kind].join('\n');
      expect(text).toContain('https://matrix.example.com');
      expect(text).toContain('smr_secret');
    }
    expect(all.nixos.join('\n')).toContain('services.supermatrix-relay');
    expect(all.systemd.join('\n')).toContain('deploy/install.sh relay');
  });
});

describe('deployment health', () => {
  const relay = {online: true, connectedAt: '', remote: '', version: '', hostname: '', health: {d1: 'ok'}};
  it('follows the relay and its probe', () => {
    expect(deploymentHealth(existing, {}).health).toBe('offline');
    expect(deploymentHealth(existing, {r1: relay}).health).toBe('ok');
    expect(deploymentHealth(existing, {r1: {...relay, health: {d1: 'dial tcp: connection refused'}}})).toEqual({
      health: 'unreachable',
      detail: 'dial tcp: connection refused',
    });
    expect(deploymentHealth(existing, {r1: {...relay, health: {}}}).health).toBe('unknown');
    expect(deploymentHealth({...existing, enabled: false}, {r1: relay}).health).toBe('disabled');
  });
});
