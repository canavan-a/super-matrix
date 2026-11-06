import {Bridge, Relay, RelayState} from '../src/api';
import {
  bridgeFormFrom,
  bridgeFormToInput,
  bridgeLine,
  PUBLIC,
} from '../src/screens/BridgeScreen';
import {bridgesOf} from '../src/screens/HomeScreen';
import {bridgeHealth, supportsBridges} from '../src/store';

const relay = (
  id: string,
  name: string,
  features: string[] = ['bridge-tcp', 'bridge-udp'],
): Relay => ({
  id,
  name,
  createdAt: '',
  lastSeenAt: null,
  lastIp: '',
  version: '',
  hostname: '',
  online: true,
  deployments: 0,
  transport: '',
  fallback: '',
  manageDeployments: false,
  features,
  bridges: 0,
});

const state = (
  health: Record<string, string>,
  features = ['bridge-tcp', 'bridge-udp'],
): RelayState => ({
  online: true,
  connectedAt: '',
  remote: '',
  version: '',
  hostname: '',
  health,
  transport: 'quic',
  rttMs: 0,
  bytesSent: 0,
  bytesReceived: 0,
  packetsLost: 0,
  streams: 0,
  features,
});

const pg: Bridge = {
  id: 'b1',
  name: 'postgres',
  protocol: 'tcp',
  enabled: true,
  public: false,
  publicAddress: '',
  fromRelayId: 'a',
  listenHost: '127.0.0.1',
  listenPort: 15432,
  toRelayId: 'b',
  targetHost: '127.0.0.1',
  targetPort: 5432,
  udpIdleS: 300,
  ipAllowlist: [],
  domainId: '',
  subdomain: '',
  host: '',
  listenError: '',
  createdAt: '',
  updatedAt: '',
};

const relays = [relay('a', 'laptop'), relay('b', 'homelab')];

describe('bridge form', () => {
  it('round-trips a relay bridge', () => {
    expect(bridgeFormToInput(bridgeFormFrom(pg))).toEqual({
      name: 'postgres',
      protocol: 'tcp',
      enabled: true,
      public: false,
      fromRelayId: 'a',
      listenHost: '127.0.0.1',
      listenPort: 15432,
      toRelayId: 'b',
      targetHost: '127.0.0.1',
      targetPort: 5432,
      udpIdleS: 300,
      ipAllowlist: [],
    });
  });

  it('sends a public bridge without a from relay, with its allow list', () => {
    const f = {
      ...bridgeFormFrom(null),
      name: 'turn',
      protocol: 'udp' as const,
      from: PUBLIC,
      toRelayId: 'b',
      targetPort: '3478',
      ipAllowlist: '10.0.0.0/8\n 203.0.113.7 ',
    };
    const input = bridgeFormToInput(f);
    expect(input.public).toBe(true);
    expect(input.fromRelayId).toBeUndefined();
    expect(input.listenPort).toBe(3478); // defaults to the target port
    expect(input.ipAllowlist).toEqual(['10.0.0.0/8', '203.0.113.7']);
    expect(input.domainId).toBe('');

    // A DNS name: the label is sent lowercased; a relay bridge never sends one.
    const named = bridgeFormToInput({
      ...f,
      domainId: 'dom1',
      subdomain: ' Turn ',
    });
    expect([named.domainId, named.subdomain]).toEqual(['dom1', 'turn']);
    const relayOnly = bridgeFormToInput({...f, from: 'a', domainId: 'dom1'});
    expect(relayOnly.domainId).toBeUndefined();
  });

  it('describes the path', () => {
    const name = (id: string) => relays.find(r => r.id === id)!.name;
    expect(bridgeLine(pg, name)).toBe(
      'client laptop 127.0.0.1:15432 → server homelab 127.0.0.1:5432',
    );
    const pub = {
      ...pg,
      public: true,
      fromRelayId: '',
      listenHost: '',
      publicAddress: 'turn.example.com:15432',
    };
    expect(bridgeLine(pub, name)).toBe(
      'turn.example.com:15432 → server homelab 127.0.0.1:5432',
    );
  });
});

describe('bridge health', () => {
  it('is ok when both ends are', () => {
    const live = {a: state({b1: 'ok'}), b: state({b1: 'ok'})};
    expect(bridgeHealth(pg, relays, live, {}).health).toBe('ok');
  });

  it('names the end that is down or out of date', () => {
    expect(bridgeHealth(pg, relays, {a: state({b1: 'ok'})}, {})).toEqual({
      health: 'offline',
      detail: 'homelab is offline',
    });
    const old = {a: state({}, []), b: state({b1: 'ok'})};
    expect(bridgeHealth(pg, relays, old, {}).detail).toBe(
      'laptop needs updating for TCP bridges',
    );
  });

  it('shows why the port could not be bound', () => {
    const live = {
      a: state({b1: 'listen: address already in use'}),
      b: state({b1: 'ok'}),
    };
    expect(bridgeHealth(pg, relays, live, {})).toEqual({
      health: 'unreachable',
      detail: 'listen: address already in use',
    });
  });

  it('checks a public bridge against the server', () => {
    const pub = {...pg, public: true, fromRelayId: ''};
    expect(
      bridgeHealth(pub, relays, {b: state({b1: 'ok'})}, {b1: 'ok'}).health,
    ).toBe('ok');
  });

  it('knows which relays can serve which protocol', () => {
    expect(supportsBridges(relay('x', 'x', ['bridge-tcp']), 'udp')).toBe(false);
    expect(supportsBridges(relay('x', 'x', ['bridge-tcp']), 'tcp')).toBe(true);
    expect(supportsBridges(undefined, 'tcp')).toBe(false);
  });
});

describe('bridges by relay', () => {
  it('lists a bridge under both of its relays, a public one only where it leads', () => {
    const pub = {...pg, id: 'b2', public: true, fromRelayId: ''};
    expect(bridgesOf('a', [pg, pub]).map(x => [x.bridge.id, x.role])).toEqual([
      ['b1', 'from'],
    ]);
    expect(bridgesOf('b', [pg, pub]).map(x => [x.bridge.id, x.role])).toEqual([
      ['b1', 'to'],
      ['b2', 'to'],
    ]);
  });
});
