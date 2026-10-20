import React, {useMemo, useRef, useState} from 'react';
import {StyleSheet, useWindowDimensions, View} from 'react-native';

import {toneColor} from '../logo/logoTheme';
import {useTheme} from '../theme';

/** How many pixels make up the constellation, over the whole screen. */
const COUNT = 33;
/** The constellation's seed: the same pattern on every phone. */
const SEED = 0x5c;

/** A seeded random stream, so the pattern is fixed rather than random each time. */
function seeded(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

/**
 * A subdued, still constellation of the logo's square pixels: fixed spots
 * from a fixed seed, so it's the same picture for everyone. The pattern is
 * laid out over the whole screen, and each PixelField (behind the header,
 * the room list, the chat) draws just its own slice of it, so it carries on
 * seamlessly from one to the next. Nothing moves, so it costs nothing once
 * drawn.
 */
export function PixelField(): React.JSX.Element {
  const theme = useTheme();
  const win = useWindowDimensions();
  const box = useRef<View>(null);
  // Where this slice sits on the screen.
  const [at, setAt] = useState<{x: number; y: number; w: number; h: number} | null>(null);

  const pixels = useMemo(() => {
    const rnd = seeded(SEED);
    const colors = [toneColor(theme, 4, 1), toneColor(theme, 3.5, 0.5), toneColor(theme, 3, 0)];
    return Array.from({length: COUNT}, (_, i) => {
      const depth = 0.35 + rnd() * 0.65; // 1 = nearest
      return {
        x: (i + rnd() * 0.8) / COUNT, // spread across the width
        y: rnd(),
        size: Math.round(2 + depth * 4),
        opacity: 0.06 + depth * 0.12,
        color: colors[Math.floor(rnd() * colors.length)],
      };
    });
  }, [theme]);

  return (
    <View
      ref={box}
      style={[StyleSheet.absoluteFill, styles.clip]}
      pointerEvents="none"
      onLayout={() => box.current?.measureInWindow((x, y, w, h) => setAt({x, y, w, h}))}>
      {at &&
        pixels.map((p, i) => {
          const left = p.x * win.width - at.x;
          const top = p.y * win.height - at.y;
          if (left < -8 || top < -8 || left > at.w || top > at.h) return null; // not in this slice
          return (
            <View
              key={i}
              style={{
                position: 'absolute',
                left,
                top,
                width: p.size,
                height: p.size,
                backgroundColor: p.color,
                opacity: p.opacity,
              }}
            />
          );
        })}
    </View>
  );
}

const styles = StyleSheet.create({
  clip: {overflow: 'hidden'},
});
