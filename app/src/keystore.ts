// Where the app keeps its sign-ins on Android: the Keystore, via
// react-native-keychain. A sign-in is a server's admin session token (from
// the root password); the password itself is never stored.
import * as Keychain from 'react-native-keychain';

const SERVICE = 'supermatrix.sessions.v1';

/** Every saved session, server URL → token. */
export async function loadSessions(): Promise<Record<string, string>> {
  try {
    const creds = await Keychain.getGenericPassword({service: SERVICE});
    return creds ? JSON.parse(creds.password) : {};
  } catch {
    return {};
  }
}

export async function saveSessions(sessions: Record<string, string>): Promise<void> {
  if (Object.keys(sessions).length === 0) {
    await Keychain.resetGenericPassword({service: SERVICE}).catch(() => {});
    return;
  }
  await Keychain.setGenericPassword('supermatrix', JSON.stringify(sessions), {
    service: SERVICE,
    accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
    storage: Keychain.STORAGE_TYPE.AES,
  });
}
