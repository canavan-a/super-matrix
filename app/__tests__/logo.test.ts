import fs from 'fs';
import path from 'path';

import {describe, expect, it} from '@jest/globals';

import palette from '../src/logo/iconPalette.json';
import {LOGO} from '../src/logo/logoData';
import {hexToRgb, isLightTheme, shineColor, shineTarget, themeAccent, themeRamp, toneColor} from '../src/logo/logoTheme';
import {SPLASH_TIMING, SPLASH_TOTAL, pulseRange} from '../src/logo/SplashScreen';
import {SHINE_STRIPS, STRIP_SPANS, WORD_STRIPS, WORD_TONES, sliceTones} from '../src/logo/LogoArt';
import {DEFAULT_ICON, wantedIcon} from '../src/appIcon';
import {THEME_OPTIONS, THEMES} from '../src/theme';

const close = (a: string, b: string, tol = 1) => {
  const x = hexToRgb(a);
  const y = hexToRgb(b);
  return x.every((v, i) => Math.abs(v - y[i]) <= tol);
};

describe('logo colors agree with the generator (Go) and with theme.tsx', () => {
  it('has a palette for every app theme and no others', () => {
    expect(Object.keys(palette.themes).sort()).toEqual(THEME_OPTIONS.map(o => o.name).sort());
  });

  for (const {name} of THEME_OPTIONS) {
    it(`${name}: ramp, accent and icon tones match`, () => {
      const theme = THEMES[name];
      const p = (palette.themes as Record<string, any>)[name];
      expect(p.bg).toBe(theme.bg);
      themeRamp(theme).forEach((c, i) => {
        const hex = '#' + c.map(v => Math.round(v).toString(16).padStart(2, '0')).join('');
        expect(close(hex, p.ramp[i])).toBe(true);
      });
      const acc = themeAccent(theme);
      expect(close('#' + acc.map(v => Math.round(v).toString(16).padStart(2, '0')).join(''), p.accent)).toBe(true);
      expect(p.iconTones.length).toBeGreaterThanOrEqual(1);
      for (const t of p.iconTones) {
        const mine = toneColor(theme, t.u, t.w);
        expect({name, u: t.u, w: t.w, ok: close(mine, t.color)}).toEqual({name, u: t.u, w: t.w, ok: true});
      }
    });
  }

  it('maps the art page to the theme page, and its strokes to the text color', () => {
    for (const {name} of THEME_OPTIONS) {
      const t = THEMES[name];
      expect(toneColor(t, 0, 0)).toBe(t.bg.toLowerCase());
      expect(close(toneColor(t, 4, 0), t.text)).toBe(true);
    }
  });

  it('shines toward white on dark themes and toward black on light ones', () => {
    for (const {name} of THEME_OPTIONS) {
      const t = THEMES[name];
      expect(shineTarget(t)).toBe(isLightTheme(t) ? '#000000' : '#ffffff');
      const base = toneColor(t, 3, 0);
      const lit = shineColor(base, t, 0.5);
      const lumOf = (h: string) => hexToRgb(h).reduce((a, v, i) => a + v * [0.2126, 0.7152, 0.0722][i], 0);
      expect(isLightTheme(t) ? lumOf(lit) < lumOf(base) : lumOf(lit) > lumOf(base)).toBe(true);
    }
    expect(isLightTheme(THEMES.light)).toBe(true);
    expect(isLightTheme(THEMES.sepia)).toBe(true);
    expect(isLightTheme(THEMES.dark)).toBe(false);
    expect(isLightTheme(THEMES.ember)).toBe(false);
  });
});

describe('logo data', () => {
  const every = (tones: {u: number; w: number; runs: number[]}[], rows: number, fn: (x: number, y: number, len: number) => void) => {
    for (const t of tones) {
      expect(t.u).toBeGreaterThanOrEqual(0);
      expect(t.u).toBeLessThanOrEqual(4);
      expect(t.w).toBeGreaterThanOrEqual(0);
      expect(t.w).toBeLessThanOrEqual(1);
      expect(t.runs.length % 3).toBe(0);
      for (let i = 0; i < t.runs.length; i += 3) {
        const [x, y, len] = [t.runs[i], t.runs[i + 1], t.runs[i + 2]];
        expect(y).toBeGreaterThanOrEqual(0);
        expect(y).toBeLessThan(rows);
        expect(len).toBeGreaterThan(0);
        fn(x, y, len);
      }
    }
  };

  it('keeps every run inside the picture', () => {
    const inside = (x: number, _y: number, len: number) => {
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x + len).toBeLessThanOrEqual(LOGO.width);
    };
    every(LOGO.mark.tones, LOGO.mark.rows, inside);
    every(LOGO.dots.tones, LOGO.mark.rows, inside);
    for (const s of LOGO.word.slices) every(s.tones, LOGO.word.rows, inside);
    every(LOGO.icon.tones, LOGO.icon.h, (x, _y, len) => {
      expect(x + len).toBeLessThanOrEqual(LOGO.icon.w);
    });
  });

  it('cuts the stacked wordmark into 11 slices (S-u-p-e-r over M-a-t-r-i-x), contiguous per line, each holding only its own columns', () => {
    expect(LOGO.word.slices).toHaveLength(11);
    const firstRow = (s: (typeof LOGO.word.slices)[number]) => Math.min(...s.tones.flatMap(t => t.runs.filter((_, k) => k % 3 === 1)));
    LOGO.word.slices.forEach((s, i) => {
      expect(s.x1).toBeGreaterThan(s.x0);
      // "Matrix" starts a new line at slice 5, below all of "Super".
      if (i > 0 && i !== 5) expect(s.x0).toBe(LOGO.word.slices[i - 1].x1);
      if (i >= 5) expect(firstRow(s)).toBeGreaterThan(Math.max(...LOGO.word.slices.slice(0, 5).map(firstRow)));
      for (const t of s.tones) {
        for (let k = 0; k < t.runs.length; k += 3) {
          expect(t.runs[k]).toBeGreaterThanOrEqual(s.x0);
          expect(t.runs[k] + t.runs[k + 2]).toBeLessThanOrEqual(s.x1);
        }
      }
    });
  });

  it('has no animated dots (the broom has none)', () => {
    expect(LOGO.dots.tones).toHaveLength(0);
  });

  it('draws the broom as its six squares alone, with no tile behind them, on a 3x3 grid', () => {
    expect(LOGO.icon.w).toBe(52);
    expect(LOGO.icon.h).toBe(39);
    // Only the bright squares; each is 12 sub-pixels wide, 9 rows tall.
    expect(LOGO.icon.tones.every(t => t.u >= 3)).toBe(true);
    const runs = LOGO.icon.tones.flatMap(t => Array.from({length: t.runs.length / 3}, (_, k) => t.runs.slice(k * 3, k * 3 + 3)));
    expect(runs.every(([, , len]) => len === 12)).toBe(true);
    // 9 rows per square row, 2 squares per row of squares, 3 rows of squares.
    expect(runs).toHaveLength(9 * 2 * 3);
    const cells = new Set(runs.map(([x, y]) => `${Math.floor(y / 15)},${Math.floor(x / 20)}`));
    expect([...cells].sort()).toEqual(['0,0', '0,1', '1,0', '1,2', '2,1', '2,2']);
  });
});

describe('shine strips', () => {
  const inkOf = (tones: {runs: number[]}[]) => tones.reduce((n, t) => n + t.runs.filter((_, i) => i % 3 === 2).reduce((a, b) => a + b, 0), 0);

  it('tile the logo\'s width exactly, with no gaps or overlaps', () => {
    expect(STRIP_SPANS).toHaveLength(SHINE_STRIPS);
    expect(STRIP_SPANS[0].x0).toBe(0);
    expect(STRIP_SPANS[SHINE_STRIPS - 1].x1).toBe(LOGO.width);
    STRIP_SPANS.forEach((s, i) => {
      expect(s.x1).toBeGreaterThan(s.x0);
      if (i > 0) expect(s.x0).toBe(STRIP_SPANS[i - 1].x1);
    });
  });

  it('together hold exactly the wordmark\'s pixels (nothing lost, nothing doubled)', () => {
    const word = inkOf(WORD_TONES);
    expect(inkOf(WORD_STRIPS.flatMap(s => s.tones))).toBe(word);
    // and each strip's runs stay inside its own columns
    for (const s of WORD_STRIPS) {
      for (const t of s.tones) {
        for (let i = 0; i < t.runs.length; i += 3) {
          expect(t.runs[i]).toBeGreaterThanOrEqual(s.x0);
          expect(t.runs[i] + t.runs[i + 2]).toBeLessThanOrEqual(s.x1);
        }
      }
    }
  });

  it('sliceTones clips runs at the edges and drops tones that end up empty', () => {
    const tones = [{u: 1, w: 0, runs: [0, 0, 10, 20, 1, 5]}];
    expect(sliceTones(tones, 5, 8)).toEqual([{u: 1, w: 0, runs: [5, 0, 3]}]);
    expect(sliceTones(tones, 12, 18)).toEqual([]);
    expect(sliceTones(tones, 22, 30)).toEqual([{u: 1, w: 0, runs: [22, 1, 3]}]);
  });

  it('fade in and out over a valid, strictly increasing range, for every strip', () => {
    for (const lag of [0, 0.05]) {
      for (let i = 0; i < SHINE_STRIPS; i++) {
        const [a, b, c] = pulseRange(i, SHINE_STRIPS, lag);
        expect(a).toBeGreaterThanOrEqual(0);
        expect(c).toBeLessThanOrEqual(1);
        expect(a).toBeLessThan(b); // an interpolation's input range must strictly increase
        expect(b).toBeLessThan(c);
      }
    }
  });

  it('sweep left to right', () => {
    let last = -1;
    for (let i = 0; i < SHINE_STRIPS; i++) {
      const [, c] = pulseRange(i, SHINE_STRIPS, 0);
      expect(c).toBeGreaterThan(last);
      last = c;
    }
  });
});

describe('splash timeline', () => {
  const T = SPLASH_TIMING;
  it('brings the letters in before the shine starts, and the shine finishes before the exit', () => {
    const lettersDone = T.letterStart + (LOGO.word.slices.length - 1) * T.letterStagger + T.letterDur;
    expect(lettersDone).toBeLessThanOrEqual(T.shineStart);
    expect(T.shineStart + T.shineDur).toBeLessThanOrEqual(T.hold);
  });
  it('is short enough not to be annoying', () => {
    expect(SPLASH_TOTAL).toBeLessThanOrEqual(3000);
    expect(SPLASH_TOTAL).toBe(T.hold + T.exit);
  });
});

describe('launcher icon', () => {
  it('follows the theme, or stays on the default when switched off', () => {
    expect(wantedIcon('ember', true)).toBe('ember');
    expect(wantedIcon('ember', false)).toBe(DEFAULT_ICON);
  });

  const res = path.join(__dirname, '../android/app/src/main/res');
  const manifest = fs.readFileSync(path.join(__dirname, '../android/app/src/main/AndroidManifest.xml'), 'utf8');

  it('declares exactly one launcher alias per theme, with only the default enabled', () => {
    const aliases = manifest.match(/<activity-alias[\s\S]*?<\/activity-alias>/g) ?? [];
    expect(aliases).toHaveLength(THEME_OPTIONS.length);
    const enabled: string[] = [];
    for (const {name} of THEME_OPTIONS) {
      const cap = name[0].toUpperCase() + name.slice(1);
      const a = aliases.find(x => x.includes(`android:name=".Icon${cap}"`));
      expect(a).toBeDefined();
      expect(a).toContain('android.intent.category.LAUNCHER');
      expect(a).toContain(`@mipmap/ic_launcher_${name}"`);
      expect(a).toContain(`@mipmap/ic_launcher_${name}_round"`);
      if (a!.includes('android:enabled="true"')) enabled.push(name);
    }
    expect(enabled).toEqual([DEFAULT_ICON]);
  });

  it('no longer launches MainActivity directly (only through the aliases)', () => {
    const activity = manifest.match(/<activity\b[\s\S]*?(\/>|<\/activity>)/)![0];
    expect(activity).not.toContain('LAUNCHER');
  });

  it('ships every icon: adaptive XML + vector, and legacy PNGs at every density', () => {
    for (const {name} of THEME_OPTIONS) {
      for (const suffix of ['', '_round']) {
        expect(fs.existsSync(path.join(res, `mipmap-anydpi-v26/ic_launcher_${name}${suffix}.xml`))).toBe(true);
        for (const d of ['mdpi', 'hdpi', 'xhdpi', 'xxhdpi', 'xxxhdpi']) {
          expect(fs.existsSync(path.join(res, `mipmap-${d}/ic_launcher_${name}${suffix}.png`))).toBe(true);
        }
      }
      expect(fs.existsSync(path.join(res, `drawable/ic_launcher_fg_${name}.xml`))).toBe(true);
    }
    expect(fs.existsSync(path.join(res, 'drawable/ic_launcher_mono.xml'))).toBe(true);
  });

  it('uses each theme\'s real page color as its icon background', () => {
    const colors = fs.readFileSync(path.join(res, 'values/ic_theme_icons.xml'), 'utf8');
    for (const {name} of THEME_OPTIONS) {
      const m = colors.match(new RegExp(`name="ic_bg_${name}">(#[0-9a-fA-F]{6})<`));
      expect(m).not.toBeNull();
      expect(m![1].toLowerCase()).toBe(THEMES[name].bg.toLowerCase());
    }
  });

  it('has the native module registered', () => {
    const app = fs.readFileSync(path.join(__dirname, '../android/app/src/main/java/com/supermatrixapp/MainApplication.kt'), 'utf8');
    expect(app).toContain('AppIconPackage()');
  });
});
