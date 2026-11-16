import {AppliedNixService, NixReady, NixService, Relay} from '../src/api';
import {phaseLabel, phaseSentence, progressLine} from '../src/nix/phase';
import {envText, lockInputs, parseEnv} from '../src/screens/NixServiceScreen';
import {nixLine} from '../src/screens/RelaysScreen';
import {allApplied, mergeApplied} from '../src/store';

const ready = (over: Partial<NixReady> = {}): NixReady => ({
  supported: true,
  agent: true,
  host: {ok: true, version: '2.24.9', flakes: true, daemon: true, systemd: true, system: 'x86_64-linux', checkedAt: ''},
  allowed: true,
  ready: true,
  step: '',
  reason: '',
  ...over,
});

const relay = (nix?: NixReady, over: Partial<Relay> = {}): Relay => ({
  id: 'r1',
  name: 'homelab',
  createdAt: '',
  lastSeenAt: '2026-01-01T00:00:00Z',
  lastIp: '',
  version: '',
  hostname: '',
  online: true,
  deployments: 0,
  transport: '',
  fallback: '',
  manageDeployments: false,
  features: [],
  bridges: 0,
  nix,
  ...over,
});

const applied = (over: Partial<AppliedNixService> = {}): AppliedNixService => ({
  id: 'a1',
  nixServiceId: 's1',
  relayId: 'r1',
  relaySlug: 'homelab',
  relayName: 'homelab',
  slug: 'kuma',
  desired: 'running',
  env: {},
  removing: false,
  phase: 'running',
  phaseSince: '',
  failing: false,
  serving: '/nix/store/abc-kuma',
  previous: '',
  lastError: '',
  missingEnv: [],
  restarts: 0,
  progress: null,
  upToDate: true,
  online: true,
  ...over,
});

const service = (as: AppliedNixService[]): NixService => ({
  id: 's1',
  slug: 'kuma',
  name: 'Kuma',
  run: 'package',
  attr: 'default',
  command: [],
  env: {},
  relayEnv: [],
  restart: 'always',
  removeState: true,
  files: {'flake.nix': '{}'},
  flakeLock: '',
  lockPinned: false,
  createdAt: '',
  updatedAt: '',
  applied: as,
});

describe('env lines', () => {
  it('reads KEY=value lines, keeping = in values', () => {
    expect(parseEnv('PORT=3001\n# a comment\n\nURL=http://x/?a=b\n')).toEqual({PORT: '3001', URL: 'http://x/?a=b'});
  });
  it('writes them back sorted', () => {
    expect(envText({B: '2', A: '1'})).toBe('A=1\nB=2');
    expect(parseEnv(envText({A: 'x=y'}))).toEqual({A: 'x=y'});
  });
});

describe('live status', () => {
  it('puts updates in place, leaving the rest alone', () => {
    const s = [service([applied(), applied({id: 'a2', relayName: 'pi'})]), service([])];
    const out = mergeApplied(s, [applied({id: 'a2', phase: 'building', progress: {step: 'building', built: 1, toBuild: 3, downloaded: 0, toDownload: 0, elapsedS: 5}})]);
    expect(out[0].applied[1].phase).toBe('building');
    expect(out[0].applied[0]).toBe(s[0].applied[0]);
    expect(out[1]).toBe(s[1]);
    expect(allApplied(out).map(x => x.applied.id)).toEqual(['a1', 'a2']);
  });
});

describe('phases', () => {
  it('reads as sentences', () => {
    expect(phaseSentence(applied({phase: 'missing-env', missingEnv: ['DB_PASSWORD']}))).toBe(
      'Set DB_PASSWORD in /etc/supermatrix/nix-services/kuma.env on homelab',
    );
    expect(phaseSentence(applied({phase: 'build-failed', failing: true}))).toMatch(/previous version keeps running/);
    expect(phaseSentence(applied({phase: 'build-failed', serving: ''}))).toBe('The build failed');
    expect(phaseSentence(applied({phase: 'tearing-down', removing: true, online: false}))).toMatch(/once homelab is back online/);
    expect(phaseLabel('crash-loop')).toBe('crash loop');
  });
  it('shows build progress', () => {
    expect(progressLine({step: 'building', built: 3, toBuild: 12, downloaded: 0, toDownload: 0, current: 'nodejs-20', elapsedS: 94})).toBe(
      'built 3/12 · nodejs-20 · 1m 34s',
    );
    expect(progressLine({step: 'evaluating', built: 0, toBuild: 0, downloaded: 0, toDownload: 0, elapsedS: 0})).toBe('evaluating');
  });
});

describe('relay readiness', () => {
  it('says what a relay needs', () => {
    expect(nixLine(relay(undefined))).toBeNull();
    expect(nixLine(relay(ready(), {nixServices: 2, nixFailing: 1}))).toBe('Nix services: on · nix 2.24.9 · 2 here, 1 failing');
    expect(nixLine(relay(ready({ready: false, step: 'update', supported: false})))).toBe('Nix services: update this relay first');
    expect(nixLine(relay(ready({ready: false, step: 'update'}), {lastSeenAt: null}))).toBe('Nix services: once it has connected');
    expect(nixLine(relay(ready({ready: false, step: 'agent', agent: false})))).toBe('Nix services: no Nix agent on its machine');
    expect(nixLine(relay(ready({ready: false, step: 'allow', allowed: false})))).toBe('Nix services: off');
  });
});

describe('pending locks', () => {
  it('say what they pin', () => {
    const lock = JSON.stringify({
      root: 'root',
      version: 7,
      nodes: {
        root: {inputs: {nixpkgs: 'nixpkgs', tools: 'tools'}},
        nixpkgs: {locked: {type: 'github', owner: 'NixOS', repo: 'nixpkgs', rev: '5d2b81a7a3bef00d', lastModified: 1759276800}},
        tools: {locked: {type: 'tarball', url: 'https://example.com/t.tar.gz', narHash: 'sha256-Qiji3lsueSxl/ngykvc6AEQ'}},
      },
    });
    expect(lockInputs(lock)).toEqual([
      'nixpkgs: github NixOS/nixpkgs @ 5d2b81a (2025-10-01)',
      'tools: tarball https://example.com/t.tar.gz sha256-Qiji3lsueSxl…',
    ]);
    expect(lockInputs('not json')).toEqual([]);
  });
});
