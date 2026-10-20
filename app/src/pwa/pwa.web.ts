// The installable web app (PWA): registers the service worker (pwa/sw.js),
// keeps the browser chrome in the theme's color, and offers installing.
//
// Updates download by themselves in the background. Switching to a new
// version reloads the page, so it only happens when that interrupts nothing
// (see App.tsx). A version that arrives while you're using the app is applied
// on the next launch, or from Settings.
import {useSyncExternalStore} from 'react';

import type {PwaState} from './pwa';

export type {PwaState};

const CHECK_EVERY_MS = 30 * 60_000;

let state: PwaState = {
  updateReady: false,
  standalone: isStandalone(),
  canPrompt: false,
  ios: isIos(),
};
const listeners = new Set<() => void>();
function set(patch: Partial<PwaState>) {
  state = {...state, ...patch};
  listeners.forEach(l => l());
}

let waiting: ServiceWorker | null = null;
let busy: () => boolean = () => true;
let reloading = false;
let deferredPrompt: any = null;

function isStandalone(): boolean {
  if (typeof window === 'undefined') return false;
  return (
    window.matchMedia?.('(display-mode: standalone)').matches ||
    (navigator as any).standalone === true
  );
}

function isIos(): boolean {
  if (typeof navigator === 'undefined') return false;
  // iPadOS reports itself as a Mac; touch gives it away.
  return (
    /iPhone|iPad|iPod/.test(navigator.userAgent) ||
    (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
  );
}

/**
 * isBusy: true while switching versions (a reload) would interrupt someone.
 */
export function startPwa(isBusy: () => boolean) {
  busy = isBusy;
  if (typeof window === 'undefined') return;

  window.addEventListener('beforeinstallprompt', (e: Event) => {
    e.preventDefault();
    deferredPrompt = e;
    set({canPrompt: true});
  });
  window.addEventListener('appinstalled', () => {
    deferredPrompt = null;
    set({canPrompt: false, standalone: true});
  });

  // Ask the browser to keep this app's storage (the encrypted key, room
  // caches) rather than clear it under storage pressure.
  navigator.storage?.persist?.().catch(() => {});

  // Not in the Vite dev server: there, files change on every save.
  if (!('serviceWorker' in navigator) || process.env.NODE_ENV !== 'production')
    return;

  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (reloading) return;
    reloading = true;
    window.location.reload();
  });

  navigator.serviceWorker
    .register('/sw.js', {scope: '/'})
    .then(reg => {
      const noteWaiting = () => {
        // Only an update: the first install has no older version to replace.
        if (reg.waiting && navigator.serviceWorker.controller) {
          waiting = reg.waiting;
          set({updateReady: true});
          applyUpdateIfSafe();
        }
      };
      reg.addEventListener('updatefound', () => {
        const sw = reg.installing;
        sw?.addEventListener(
          'statechange',
          () => sw.state === 'installed' && noteWaiting(),
        );
      });
      noteWaiting();
      const check = () => reg.update().catch(() => {});
      setInterval(check, CHECK_EVERY_MS);
      document.addEventListener('visibilitychange', () => {
        if (document.visibilityState === 'visible') check();
        // Going to the background is a good moment to switch, if locked.
        else applyUpdateIfSafe();
      });
      window.addEventListener('online', check);
    })
    .catch(() => {});
}

/** Switch to the downloaded version if nothing would be interrupted. */
export function applyUpdateIfSafe() {
  if (waiting && !busy()) applyUpdateNow();
}

/** Switch now (Settings' "Restart to update"); the page reloads. */
export function applyUpdateNow() {
  if (!waiting) return;
  waiting.postMessage('skip-waiting');
}

export async function promptInstall(): Promise<boolean> {
  if (!deferredPrompt) return false;
  deferredPrompt.prompt();
  const choice = await deferredPrompt.userChoice.catch(() => null);
  deferredPrompt = null;
  set({canPrompt: false});
  return choice?.outcome === 'accepted';
}

/** The status bar / title bar and the page behind the app, in the theme's background. */
export function setThemeColor(color: string) {
  if (typeof document === 'undefined') return;
  document
    .querySelector('meta[name="theme-color"]')
    ?.setAttribute('content', color);
  document.documentElement.style.background = color;
  document.body.style.background = color;
}

export function usePwa(): PwaState {
  return useSyncExternalStore(
    l => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => state,
    () => state,
  );
}
