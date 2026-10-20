// The web build keeps sign-ins in localStorage. The web app is normally
// served by the Super Matrix server itself, so its origin is the server's.
const KEY = 'supermatrix.sessions.v1';

export async function loadSessions(): Promise<Record<string, string>> {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}');
  } catch {
    return {};
  }
}

export async function saveSessions(sessions: Record<string, string>): Promise<void> {
  try {
    if (Object.keys(sessions).length === 0) localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, JSON.stringify(sessions));
  } catch {
    // private mode or storage disabled: the session lasts this tab only
  }
}
