import {ApiError, SchemaStatus} from '../src/api';
import {problemsOf} from '../src/screens/SchemaScreen';
import {locked, lockedEntries, overrideOn, strictCollection} from '../src/store';

const now = Date.parse('2026-11-12T12:00:00Z');

function status(override: SchemaStatus['override']): SchemaStatus {
  return {
    revision: 3,
    appliedAt: '2026-11-12T11:00:00Z',
    appliedBy: 'token:ci',
    docHash: 'h',
    strict: ['groups.ip'],
    override,
    versions: {min: 1, current: 1},
  };
}

const off = status({on: false, until: null});
const on = status({on: true, until: '2026-11-12T13:00:00Z'});
const expired = status({on: true, until: '2026-11-12T11:59:00Z'});
const managed = {revision: 3, fields: ['target_port'], entries: {ip_allowlist: ['10.0.0.1/32']}};

test('override is on only until it ends', () => {
  expect(overrideOn(off, now)).toBe(false);
  expect(overrideOn(on, now)).toBe(true);
  expect(overrideOn(expired, now)).toBe(false);
  expect(overrideOn(null, now)).toBe(false);
});

test('locks follow what the schema manages', () => {
  expect(locked(managed, 'target_port', off)).toBe(true);
  expect(locked(managed, 'enabled', off)).toBe(false);
  expect(locked(null, 'target_port', off)).toBe(false);
  expect(lockedEntries(managed, 'ip_allowlist', off)).toEqual(['10.0.0.1/32']);
  expect(lockedEntries(managed, 'otp_emails', off)).toEqual([]);
  expect(strictCollection(off, 'groups.ip')).toBe(true);
  expect(strictCollection(off, 'deployments')).toBe(false);
  expect(strictCollection(null, 'groups.ip')).toBe(false);
});

test('override unlocks everything', () => {
  jest.useFakeTimers().setSystemTime(now);
  expect(locked(managed, 'target_port', on)).toBe(false);
  expect(lockedEntries(managed, 'ip_allowlist', on)).toEqual([]);
  expect(strictCollection(on, 'groups.ip')).toBe(false);
  jest.useRealTimers();
});

test('schema problems come from the error body', () => {
  const e = new ApiError(400, 'x', {errors: [{path: 'deployments.a.relay', msg: 'no relay'}]});
  expect(problemsOf(e)).toEqual([{path: 'deployments.a.relay', msg: 'no relay'}]);
  expect(problemsOf(new Error('offline'))).toEqual([{path: '', msg: 'offline'}]);
});

test("override ends by the server's count, not the device clock", () => {
  // The device clock is an hour behind: by it, until is two hours off,
  // but the server said a minute was left when the status came.
  const skewed = status({on: true, until: '2026-11-12T14:00:00Z', secondsLeft: 60, endsAt: now + 60_000});
  expect(overrideOn(skewed, now)).toBe(true);
  expect(overrideOn(skewed, now + 61_000)).toBe(false);
});
