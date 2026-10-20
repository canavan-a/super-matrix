import {Relay, RelayState, ServerInfo} from '../src/api';
import {formatBytes, formatRtt, isFallback, linkBadge, linkDetails, quicHint, transportBadge, viaLine} from '../src/link';

const relay = (over: Partial<Relay> = {}): Relay => ({
  id: 'r1',
  name: 'homelab',
  createdAt: '',
  lastSeenAt: null,
  lastIp: '',
  version: '',
  hostname: '',
  online: false,
  deployments: 0,
  transport: '',
  fallback: '',
  ...over,
});

const state = (over: Partial<RelayState> = {}): RelayState => ({
  online: true,
  connectedAt: '',
  remote: '198.51.100.7',
  version: '0.1.0',
  hostname: 'pi',
  health: {},
  transport: 'quic',
  rttMs: 23.4,
  bytesSent: 1_500,
  bytesReceived: 12_300_000,
  packetsLost: 0,
  streams: 1,
  ...over,
});

const info = (quic: ServerInfo['quic']): ServerInfo => ({
  adminHost: 'm',
  tls: true,
  version: '',
  serverUrl: '',
  quic,
});

describe('link', () => {
  it('formats round trips and sizes', () => {
    expect(formatRtt(0)).toBe('');
    expect(formatRtt(0.4)).toBe('<1 ms');
    expect(formatRtt(4.26)).toBe('4.3 ms');
    expect(formatRtt(23.4)).toBe('23 ms');
    expect(formatBytes(999)).toBe('999 B');
    expect(formatBytes(1_500)).toBe('1.5 KB');
    expect(formatBytes(12_300_000)).toBe('12 MB');
  });

  it('badges a relay by its live transport, or the last one', () => {
    expect(linkBadge(relay(), state())).toBe('QUIC · 23 ms');
    expect(linkBadge(relay(), state({transport: 'websocket', rttMs: 0}))).toBe('WebSocket');
    expect(linkBadge(relay({transport: 'websocket'}), undefined)).toBe('last on WebSocket');
    expect(linkBadge(relay(), undefined)).toBe('');
  });

  it('flags a fallback only when the server offers QUIC', () => {
    const ws = state({transport: 'websocket'});
    expect(isFallback(info({addr: 'm:443', serverName: 'm'}), ws)).toBe(true);
    expect(isFallback(info(null), ws)).toBe(false);
    expect(isFallback(info({addr: 'm:443', serverName: 'm'}), state())).toBe(false);
    expect(
      isFallback(info({addr: 'm:443', serverName: 'm'}), state({transport: 'websocket', fallback: 'transport set to websocket'})),
    ).toBe(false);
  });

  it('explains the link', () => {
    expect(linkDetails(relay(), state({packetsLost: 3, streams: 2}))).toEqual([
      'Connected over QUIC from 198.51.100.7',
      'round trip 23 ms · 12 MB in, 1.5 KB out · 3 packets lost · 2 connections open',
    ]);
    expect(
      linkDetails(
        relay(),
        state({
          transport: 'websocket',
          fallback: 'no answer on udp m:443 within 5s (UDP blocked?)',
        }),
      )[0],
    ).toBe('Connected over the WebSocket from 198.51.100.7, not QUIC: no answer on udp m:443 within 5s (UDP blocked?)');
    expect(linkDetails(relay({transport: 'websocket', fallback: 'transport set to websocket'}), undefined)).toEqual([
      'Last connected over WebSocket (not QUIC: transport set to websocket)',
    ]);
  });

  it('says how a deployment is reached', () => {
    expect(transportBadge(state())).toBe('QUIC · 23 ms');
    expect(viaLine('homelab', state())).toBe('Via homelab over QUIC · 23 ms');
    expect(viaLine('homelab', state({transport: 'websocket', fallback: 'UDP blocked', rttMs: 0}))).toBe(
      'Via homelab over the WebSocket (not QUIC: UDP blocked)',
    );
    expect(viaLine('homelab', undefined)).toBe('Via homelab (offline)');
  });

  it('says what the server offers', () => {
    expect(quicHint(info(null))).toMatch(/QUIC is off/);
    expect(quicHint(info({addr: 'm:443', serverName: 'm'}))).toMatch(/UDP m:443/);
  });
});
