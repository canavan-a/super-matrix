import React, {useMemo, useRef, useState} from 'react';
import {Animated, PanResponder, Platform, StyleSheet, View} from 'react-native';

import {useTheme} from '../theme';
import {TypingDots} from './TypingDots';

/** How far to pull (after resistance) before letting go refreshes. */
const TRIGGER = 70;
/** The dots stop after this even if the refresh is still going. */
const MAX_SPIN_MS = 2500;
/** The gap the list holds open, with the dots in it, while refreshing. */
const HOLD = 56;

/** Rubber-band resistance: follows the finger at first, then less and less. */
const rubber = (dy: number) => 140 * (1 - Math.exp(-Math.max(0, dy) / 200));

/**
 * A springy pull-to-refresh around a scrollable list. Pulled down from the
 * top, the list follows the finger with rubber-band resistance while the
 * app's three pixel dots grow in above it; let go past the line and it
 * springs back to a small gap that holds the bouncing dots while
 * `onRefresh` runs, then closes. No text, and the list stays usable. `atTop` says whether the list is scrolled to its
 * top (the pull only starts there).
 */
export function SpringyPull({
  atTop,
  onRefresh,
  background,
  children,
}: {
  atTop: () => boolean;
  onRefresh: () => Promise<void>;
  /** Drawn behind everything and given the live pull distance (e.g. PixelField). */
  background?: (pull: Animated.Value) => React.ReactNode;
  children: React.ReactNode;
}): React.JSX.Element {
  const theme = useTheme();
  const pull = useRef(new Animated.Value(0)).current;
  const [phase, setPhase] = useState<'idle' | 'pulling' | 'armed' | 'refreshing'>('idle');
  const busy = useRef(false);
  const useNative = Platform.OS !== 'web';

  const springTo = (to: number, done?: () => void) =>
    Animated.spring(pull, {toValue: to, useNativeDriver: useNative, tension: 120, friction: to === 0 ? 7 : 9}).start(done);

  // On release the list springs back to a gap holding the bouncing dots,
  // stays usable, and closes once the refresh is done.
  const refresh = async () => {
    busy.current = true;
    setPhase('refreshing');
    springTo(HOLD);
    // A beat of bounce even when the refresh is instant, so it reads; and
    // never more than a couple of seconds: whatever is still loading after
    // that lands quietly on its own.
    const work = onRefresh().catch(() => {});
    await Promise.all([
      Promise.race([work, new Promise(r => setTimeout(r, MAX_SPIN_MS))]),
      new Promise(r => setTimeout(r, 600)),
    ]);
    busy.current = false;
    springTo(0, () => {
      if (busy.current) return;
      pull.setValue(0); // settle exactly closed, whatever the animation reported
      setPhase('idle');
    });
  };

  const responder = useMemo(
    () =>
      PanResponder.create({
        // Claim the drag before the list does, but only for a downward pull
        // at the top. Pulling works at any time, a refresh in progress
        // included: then it just springs back.
        // A clear downward drag only: a tap's few pixels of wobble mustn't
        // be taken for a pull (that swallowed taps on the rooms).
        onMoveShouldSetPanResponderCapture: (_e, g) =>
          atTop() && g.dy > 14 && Math.abs(g.dy) > Math.abs(g.dx) * 2,
        onPanResponderGrant: () => {
          if (!busy.current) setPhase('pulling');
        },
        onPanResponderMove: (_e, g) => {
          const d = rubber(g.dy);
          pull.setValue(d);
          if (!busy.current) setPhase(p => (d >= TRIGGER ? (p === 'armed' ? p : 'armed') : p === 'pulling' ? p : 'pulling'));
        },
        onPanResponderRelease: (_e, g) => {
          if (!busy.current && rubber(g.dy) >= TRIGGER) refresh();
          else if (busy.current) springTo(HOLD); // still refreshing: keep the gap
          else springTo(0, () => !busy.current && setPhase('idle'));
        },
        onPanResponderTerminate: () =>
          busy.current ? springTo(HOLD) : springTo(0, () => !busy.current && setPhase('idle')),
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const progress = pull.interpolate({inputRange: [0, TRIGGER], outputRange: [0, 1], extrapolate: 'clamp'});
  return (
    <View style={styles.fill} {...responder.panHandlers}>
      {background?.(pull)}
      <Animated.View
        pointerEvents="none"
        style={[
          styles.indicator,
          {
            // While refreshing, the bouncing dots below take its place.
            opacity: phase === 'refreshing' ? 0 : progress,
            transform: [
              {translateY: pull.interpolate({inputRange: [0, 140], outputRange: [-24, 46], extrapolate: 'clamp'})},
              {scale: progress.interpolate({inputRange: [0, 1], outputRange: [0.6, 1]})},
            ],
          },
        ]}>
        <View style={styles.dots}>
          {[0, 1, 2].map(i => (
            <View key={i} style={[styles.dot, {backgroundColor: phase === 'armed' ? theme.primary : theme.textMuted}]} />
          ))}
        </View>
      </Animated.View>
      {phase === 'refreshing' && (
        // In the gap above the list, fading out as it closes.
        <Animated.View
          key="busy"
          pointerEvents="none"
          style={[styles.busy, {opacity: pull.interpolate({inputRange: [0, HOLD], outputRange: [0, 1], extrapolate: 'clamp'})}]}>
          <TypingDots color={theme.primary} size={6} />
        </Animated.View>
      )}
      {/* Keyed: without it, adding the dots before it while refreshing made
          React remount the list's container mid-animation, which then kept
          the held gap open for good. */}
      <Animated.View key="list" style={[styles.fill, {transform: [{translateY: pull}]}]}>
        {children}
      </Animated.View>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: {flex: 1},
  indicator: {position: 'absolute', top: 0, left: 0, right: 0, alignItems: 'center', gap: 6},
  dots: {flexDirection: 'row', gap: 4},
  dot: {width: 7, height: 7},
  busy: {position: 'absolute', top: 0, left: 0, right: 0, height: HOLD, alignItems: 'center', justifyContent: 'center'},
});
