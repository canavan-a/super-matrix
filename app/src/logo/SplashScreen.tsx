import React, {useEffect, useMemo, useRef, useState} from 'react';
import {AccessibilityInfo, Animated, Easing, Platform, Pressable, StyleSheet, View, useWindowDimensions} from 'react-native';

import type {Theme} from '../theme';
import {LOGO_H, LOGO_W, PIXEL_W, SHINE_STRIPS, ToneLayer, WORD_H, WORD_STRIPS} from './LogoArt';
import {LOGO, ToneRuns} from './logoData';
import {shineColor, toneColor} from './logoTheme';

// The launch screen: the stacked "Super Matrix" wordmark,
// recolored for the current theme. The letters rise in one after another, then
// a shine sweeps across.
//
// Everything animated is an opacity or a translate/scale on the native driver,
// and nothing else: no skew, no clipping, no layers moving against each other.
// (The shine used to be a slanted, clipped window sliding over a counter-moving
// copy of the logo. React Native on Android drops skew from view transforms
// anyway, so it could never look right there, and it was the riskiest part of
// the screen.) The shine is now a row of thin strips of a lightened copy of the
// logo, each fading in and out as the sweep passes it.

/** Timeline, in milliseconds. */
export const SPLASH_TIMING = {
  letterStart: 120,
  letterStagger: 42,
  letterDur: 380,
  shineStart: 950,
  shineDur: 950,
  hold: 2350, // when the exit fade begins
  exit: 350,
};

/** How much of the way toward the shine target the highlight colors go. */
const SHINE_STRENGTH = 0.7;

/** How bright the highlight gets on the strip the sweep is over (its opacity). */
const SHINE_PEAK = 0.55;

/** Total running time. */
export const SPLASH_TOTAL = SPLASH_TIMING.hold + SPLASH_TIMING.exit;

/**
 * The shine's progress (0..1) at which the sweep is over strip i of n: the
 * strip fades up from nothing, peaks, and fades away. Strictly increasing, as
 * an interpolation's input range must be.
 */
export function pulseRange(i: number, n: number, lag: number): [number, number, number] {
  const c = 0.06 + (0.88 * i) / (n - 1) + lag;
  return [Math.max(0, c - 0.13), c, Math.min(1, c + 0.13)];
}

interface Props {
  theme: Theme;
  /** called when the splash has finished (or was tapped away) */
  onDone: () => void;
  /** skip the motion: hold the logo briefly, then fade */
  reduceMotion?: boolean;
  /** the app is still loading: stay up past the timeline until this goes false */
  hold?: boolean;
}

export function SplashScreen({theme, onDone, reduceMotion, hold = false}: Props): React.JSX.Element {
  const {width: screenW, height: screenH} = useWindowDimensions();
  // Kept modest, with plenty of space around it: at most 60% of the width and
  // 45% of the height (the stacked logo is a little taller than wide).
  const W = Math.min(screenW * 0.6, 320, (screenH * 0.45 * LOGO_W) / LOGO_H);
  const scale = W / LOGO_W;
  const H = LOGO_H * scale;
  const useNative = Platform.OS !== 'web';

  const [reduce, setReduce] = useState(reduceMotion ?? false);
  useEffect(() => {
    if (reduceMotion !== undefined) return;
    let alive = true;
    // Promise.resolve: never trust a platform API to hand back a promise.
    Promise.resolve(AccessibilityInfo.isReduceMotionEnabled?.())
      .then(v => alive && setReduce(!!v))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [reduceMotion]);

  // Created once. (useRef({...}) would build a fresh set of Animated.Values on
  // every render and throw all but the first away.)
  const values = useRef<{
    letters: Animated.Value[];
    shine: Animated.Value;
    exit: Animated.Value;
  } | null>(null);
  if (!values.current) {
    values.current = {
      letters: LOGO.word.slices.map(() => new Animated.Value(reduce ? 1 : 0)),
      shine: new Animated.Value(0),
      exit: new Animated.Value(1),
    };
  }
  const v = values.current;

  const finished = useRef(false);
  const skipping = useRef(false);
  const running = useRef<Animated.CompositeAnimation | null>(null);
  const done = () => {
    if (finished.current) return;
    finished.current = true;
    onDone();
  };
  // A tap fades the splash out rather than unmounting it on the spot: pulling
  // views out from under natively-driven animations that are still running is
  // a classic way to crash React Native on Android.
  const skip = () => {
    if (finished.current || skipping.current) return;
    skipping.current = true;
    running.current?.stop();
    Animated.timing(v.exit, {toValue: 0, duration: 160, easing: Easing.linear, useNativeDriver: useNative}).start(done);
  };

  const timing = (val: Animated.Value, to: number, duration: number, delay: number, easing = Easing.out(Easing.cubic)) =>
    Animated.timing(val, {toValue: to, duration, delay, easing, useNativeDriver: useNative});

  // After the intro the splash fades out, unless the app is still loading
  // behind it (`hold`): then the shine keeps sweeping, as the loading
  // indicator, until it lets go. Refs, not state, so the fade starts in the
  // same tick the intro ends.
  const holdRef = useRef(hold);
  holdRef.current = hold;
  const introDone = useRef(false);
  const exiting = useRef(false);
  const exitNow = () => {
    if (exiting.current || skipping.current) return;
    exiting.current = true;
    running.current?.stop();
    const T = SPLASH_TIMING;
    const exit = reduce ? timing(v.exit, 0, 200, 0, Easing.linear) : timing(v.exit, 0, T.exit, 0, Easing.in(Easing.quad));
    running.current = exit;
    exit.start(r => r.finished && done());
  };
  const afterIntro = () => {
    introDone.current = true;
    if (!holdRef.current) return exitNow();
    if (reduce) return; // just stay put
    const T = SPLASH_TIMING;
    v.shine.setValue(0);
    const loop = Animated.loop(
      Animated.sequence([timing(v.shine, 1, T.shineDur, 0, Easing.inOut(Easing.quad)), Animated.delay(250)]),
    );
    running.current = loop;
    loop.start();
  };

  // The intro: letters in, one shine, then the rest of the hold time.
  useEffect(() => {
    const T = SPLASH_TIMING;
    const animation = reduce
      ? Animated.delay(600)
      : Animated.parallel([
          ...v.letters.map((l, i) => timing(l, 1, T.letterDur, T.letterStart + i * T.letterStagger)),
          timing(v.shine, 1, T.shineDur, T.shineStart, Easing.inOut(Easing.quad)),
          Animated.delay(T.hold),
        ]);
    running.current = animation;
    animation.start(r => r.finished && afterIntro());
    return () => {
      running.current?.stop();
      running.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reduce]);

  // Loading finished while the splash was holding: fade out now.
  useEffect(() => {
    if (!hold && introDone.current) exitNow();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hold]);

  // colors for this theme
  const base = useMemo(() => (t: ToneRuns) => toneColor(theme, t.u, t.w), [theme]);
  const glint = useMemo(() => (t: ToneRuns) => shineColor(toneColor(theme, t.u, t.w), theme, SHINE_STRENGTH), [theme]);

  // The shine's opacities, built once so their native nodes aren't recreated.
  const fades = useMemo(
    () =>
      WORD_STRIPS.map((_, i) =>
        v.shine.interpolate({inputRange: pulseRange(i, SHINE_STRIPS, 0), outputRange: [0, SHINE_PEAK, 0], extrapolate: 'clamp'}),
      ),
    [v],
  );

  const wordH = WORD_H * scale;

  const strips = () =>
    WORD_STRIPS.map((s, i) =>
      s.tones.length === 0 ? null : (
        <Animated.View
          key={`w${i}`}
          pointerEvents="none"
          style={[
            styles.abs,
            {left: s.x0 * PIXEL_W * scale, top: 0, width: (s.x1 - s.x0) * PIXEL_W * scale, height: wordH, opacity: fades[i]},
          ]}>
          <ToneLayer
            tones={s.tones}
            fill={glint}
            view={[s.x0 * PIXEL_W, 0, (s.x1 - s.x0) * PIXEL_W, WORD_H]}
            width={(s.x1 - s.x0) * PIXEL_W * scale}
            height={wordH}
          />
        </Animated.View>
      ),
    );

  return (
    <Animated.View style={[StyleSheet.absoluteFill, {backgroundColor: theme.bg, opacity: v.exit}]} accessible accessibilityLabel="Super Matrix">
      <Pressable style={styles.center} onPress={skip} accessibilityRole="button" accessibilityLabel="Skip intro">
        <View style={{width: W, height: H}}>
          {LOGO.word.slices.map((s, i) => (
            <Animated.View
              key={i}
              style={[
                styles.abs,
                {
                  left: s.x0 * PIXEL_W * scale,
                  top: 0,
                  width: (s.x1 - s.x0) * PIXEL_W * scale,
                  height: wordH,
                  opacity: v.letters[i],
                  transform: [{translateY: v.letters[i].interpolate({inputRange: [0, 1], outputRange: [14, 0]})}],
                },
              ]}>
              <ToneLayer
                tones={s.tones}
                fill={base}
                view={[s.x0 * PIXEL_W, 0, (s.x1 - s.x0) * PIXEL_W, WORD_H]}
                width={(s.x1 - s.x0) * PIXEL_W * scale}
                height={wordH}
              />
            </Animated.View>
          ))}

          {!reduce && (
            <View style={[styles.abs, {width: W, height: H}]} pointerEvents="none">
              {strips()}
            </View>
          )}
        </View>
      </Pressable>
    </Animated.View>
  );
}

const styles = StyleSheet.create({
  center: {flex: 1, alignItems: 'center', justifyContent: 'center'},
  abs: {position: 'absolute', top: 0, left: 0},
});
