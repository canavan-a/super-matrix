// How a relay is connected to the server: QUIC or the WebSocket fallback,
// and what the link looks like (round trip, traffic, loss).

import {Relay, RelayState, ServerInfo, Transport} from './api';

export const TRANSPORT_LABEL: Record<Transport, string> = {
  quic: 'QUIC',
  websocket: 'WebSocket',
};

export function formatRtt(ms: number): string {
  if (!ms) return '';
  if (ms < 1) return '<1 ms';
  if (ms < 10) return `${ms.toFixed(1)} ms`;
  return `${Math.round(ms)} ms`;
}

export function formatBytes(n: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let v = n;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return i === 0 ? `${v} B` : `${v < 10 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

/** A live link in short: "QUIC · 23 ms". */
export function transportBadge(s: RelayState): string {
  return [TRANSPORT_LABEL[s.transport], formatRtt(s.rttMs)].filter(Boolean).join(' · ');
}

/** The short form for a relay's row: its live link, or the last transport when offline. */
export function linkBadge(r: Relay, s: RelayState | undefined): string {
  if (s) return transportBadge(s);
  return r.transport ? `last on ${TRANSPORT_LABEL[r.transport]}` : '';
}

/** The reason a relay gives when it's pinned to the WebSocket on purpose. */
const BY_CHOICE = 'transport set to websocket';

/** Whether a relay is on the WebSocket although the server offers QUIC, and not by choice (worth a look). */
export function isFallback(info: ServerInfo | null, s: RelayState | undefined): boolean {
  return !!info?.quic && s?.transport === 'websocket' && s.fallback !== BY_CHOICE;
}

/** Lines describing the link, for an expanded relay. */
export function linkDetails(r: Relay, s: RelayState | undefined): string[] {
  if (!s) {
    if (!r.transport) return [];
    const why = r.transport === 'websocket' && r.fallback ? ` (not QUIC: ${r.fallback})` : '';
    return [`Last connected over ${TRANSPORT_LABEL[r.transport]}${why}`];
  }
  const lines = [
    s.transport === 'quic'
      ? `Connected over QUIC from ${s.remote}`
      : `Connected over the WebSocket from ${s.remote}${s.fallback ? `, not QUIC: ${s.fallback}` : ''}`,
  ];
  const rtt = formatRtt(s.rttMs);
  const parts = [
    rtt && `round trip ${rtt}`,
    `${formatBytes(s.bytesReceived)} in, ${formatBytes(s.bytesSent)} out`,
    s.transport === 'quic' && s.packetsLost > 0 ? `${s.packetsLost} packets lost` : '',
    `${s.streams} connection${s.streams === 1 ? '' : 's'} open`,
  ].filter(Boolean);
  lines.push(parts.join(' · '));
  return lines;
}

/** What the server offers relays, for the top of the relays screen. */
export function quicHint(info: ServerInfo | null): string {
  if (!info) return '';
  if (!info.quic) return 'QUIC is off on this server; relays connect over the WebSocket (TCP).';
  return `Relays connect over QUIC (UDP ${info.quic.addr}) and fall back to the WebSocket where UDP is blocked.`;
}

/** One line for a deployment: which relay it goes through, and how that relay is connected. */
export function viaLine(relayName: string, s: RelayState | undefined): string {
  if (!s) return `Via ${relayName} (offline)`;
  const rtt = formatRtt(s.rttMs);
  const how = s.transport === 'quic' ? 'over QUIC' : `over the WebSocket${s.fallback ? ` (not QUIC: ${s.fallback})` : ''}`;
  return `Via ${relayName} ${how}${rtt ? ` · ${rtt}` : ''}`;
}
