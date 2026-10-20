import {dnsRecords, isWildcardInput} from '../src/domains';

describe('domains', () => {
  it('writes the records that point a domain here', () => {
    expect(dnsRecords('*.example.com', ['203.0.113.10', '2001:db8::1'])).toBe(
      '*.example.com  A  203.0.113.10\n*.example.com  AAAA  2001:db8::1',
    );
    expect(dnsRecords('status.foo.org', [])).toBe('');
  });

  it('tells wildcards from exact names', () => {
    expect(isWildcardInput(' *.example.com')).toBe(true);
    expect(isWildcardInput('status.example.com')).toBe(false);
  });
});
