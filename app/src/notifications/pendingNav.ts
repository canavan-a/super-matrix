// Where a notification tap should land, handed from index.js (which runs with
// no React tree mounted) to App.tsx. Only the update notification exists, so
// the only target is Settings.
export const SETTINGS_NAV = 'settings';

let pending: string | null = null;
const listeners = new Set<(target: string) => void>();

export const pendingNav = {
  get: () => pending,
  set(target: string) {
    pending = target;
    listeners.forEach(l => l(target));
  },
  clear() {
    pending = null;
  },
  subscribe(l: (target: string) => void): () => void {
    listeners.add(l);
    return () => listeners.delete(l);
  },
};

/** The nav target in a notification's data, if any. */
export function navTarget(data: Record<string, unknown> | undefined): string | null {
  return typeof data?.nav === 'string' ? data.nav : null;
}
