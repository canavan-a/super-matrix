// Helpers for the domains screen.

/** The DNS records that point label at the server's addresses. */
export function dnsRecords(label: string, addresses: string[]): string {
  return addresses.map(ip => `${label}  ${ip.includes(':') ? 'AAAA' : 'A'}  ${ip}`).join('\n');
}

/** Whether what's typed reads as a wildcard ("*.example.com"). */
export function isWildcardInput(s: string): boolean {
  return s.trim().startsWith('*.');
}
