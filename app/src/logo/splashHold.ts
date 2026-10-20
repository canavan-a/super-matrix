// Lets the app keep the launch splash up as its loading screen. SessionGate
// holds it at startup and lets go once there's something to show: the
// deployments have loaded, or the sign-in screen is up. Not held, the splash
// plays its normal timeline.

/** Never hold longer than this, whatever is still loading. */
export const MAX_HOLD_MS = 12_000;

type Listener = (held: boolean) => void;

let held = false;
let timer: ReturnType<typeof setTimeout> | null = null;
const listeners = new Set<Listener>();

function set(next: boolean): void {
  if (held === next) return;
  held = next;
  listeners.forEach(l => l(held));
}

export const splashHold = {
  get: (): boolean => held,
  hold(): void {
    if (timer) clearTimeout(timer);
    timer = setTimeout(() => splashHold.release(), MAX_HOLD_MS);
    set(true);
  },
  release(): void {
    if (timer) clearTimeout(timer);
    timer = null;
    set(false);
  },
  subscribe(l: Listener): () => void {
    listeners.add(l);
    return () => listeners.delete(l);
  },
};
