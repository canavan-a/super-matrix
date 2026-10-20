// Turns the logo's tones (see logoData.ts) into a theme's colors. This mirrors
// tui/internal/art/palette.go exactly — a test compares the two through the
// generated iconPalette.json — so the app, the terminal and the launcher icons
// always agree about what a theme's logo looks like.
import type {Theme} from '../theme';

type RGB = [number, number, number];

/** The theme colors the logo uses. */
export type LogoTheme = Pick<Theme, 'bg' | 'surface' | 'border' | 'textMuted' | 'text' | 'primary'>;

export function hexToRgb(hex: string): RGB {
  const v = parseInt(hex.replace('#', ''), 16);
  return [(v >> 16) & 255, (v >> 8) & 255, v & 255];
}

export function rgbToHex(c: RGB): string {
  const h = (n: number) => Math.max(0, Math.min(255, Math.round(n))).toString(16).padStart(2, '0');
  return `#${h(c[0])}${h(c[1])}${h(c[2])}`;
}

const lum = (c: RGB) => 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2];
const lerp = (a: RGB, b: RGB, t: number): RGB => [
  a[0] + (b[0] - a[0]) * t,
  a[1] + (b[1] - a[1]) * t,
  a[2] + (b[2] - a[2]) * t,
];

export function isLightTheme(t: Pick<Theme, 'bg'>): boolean {
  return lum(hexToRgb(t.bg)) > 128;
}

/** The theme's tones, dark→light in the same order as the art's own ramp:
 *  page, raised surface, badger body, mid, text. */
export function themeRamp(t: LogoTheme): RGB[] {
  const bg = hexToRgb(t.bg);
  const muted = hexToRgb(t.textMuted);
  // `border` is near-white on a light page (the badger would vanish), so the
  // body is derived instead; on dark pages it sits between border and muted.
  const body = isLightTheme(t) ? lerp(muted, bg, 0.45) : lerp(hexToRgb(t.border), muted, 0.35);
  return [bg, hexToRgb(t.surface), body, muted, hexToRgb(t.text)];
}

/** The color for the cyan parts (the eye and the wordmark's gradient). Dark
 *  themes' primaries are dull against a near-black page, so they are lifted. */
export function themeAccent(t: LogoTheme): RGB {
  const p = hexToRgb(t.primary);
  return isLightTheme(t) ? p : lerp(p, [255, 255, 255], 0.35);
}

/** A tone's color in a theme. u is the ramp position (0..4), w the accent lean (0..1). */
export function toneColor(t: LogoTheme, u: number, w: number): string {
  const r = themeRamp(t);
  const i = Math.min(3, Math.floor(u));
  const base = lerp(r[i], r[i + 1], u - i);
  const acc = themeAccent(t);
  const top = r[4];
  return rgbToHex([base[0] + w * (acc[0] - top[0]), base[1] + w * (acc[1] - top[1]), base[2] + w * (acc[2] - top[2])]);
}

/** What the shine moves toward: white on dark pages; on light pages the
 *  strokes are dark, so brightening would only fade them out — darken instead. */
export function shineTarget(t: Pick<Theme, 'bg'>): string {
  return isLightTheme(t) ? '#000000' : '#ffffff';
}

/** Blend a color toward the shine target by k (0..1). */
export function shineColor(color: string, t: Pick<Theme, 'bg'>, k: number): string {
  return rgbToHex(lerp(hexToRgb(color), hexToRgb(shineTarget(t)), k));
}
