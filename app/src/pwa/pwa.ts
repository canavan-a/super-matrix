// The installable web app only exists on the web build (pwa.web.ts); on
// Android these do nothing.
export interface PwaState {
  /** A new version is downloaded and waiting for a safe moment to switch. */
  updateReady: boolean;
  /** Running installed (home screen / app window), not in a browser tab. */
  standalone: boolean;
  /** The browser offered its own install prompt (Chrome, Edge, Android). */
  canPrompt: boolean;
  /** iOS/iPadOS Safari, where installing is Share → Add to Home Screen. */
  ios: boolean;
}

const NATIVE: PwaState = {
  updateReady: false,
  standalone: true,
  canPrompt: false,
  ios: false,
};

export function startPwa(_isBusy: () => boolean) {}
export function setThemeColor(_color: string) {}
export function applyUpdateIfSafe() {}
export function applyUpdateNow() {}
export async function promptInstall(): Promise<boolean> {
  return false;
}
export function usePwa(): PwaState {
  return NATIVE;
}
