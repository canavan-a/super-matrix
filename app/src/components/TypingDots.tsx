import React, {useEffect, useRef} from 'react';
import {Animated, StyleSheet, View} from 'react-native';

/**
 * Three square "pixels" rising in turn: Super Chat's typing dots, shown here
 * while a pull-to-refresh runs (see SpringyPull).
 */
export function TypingDots({color, size = 5}: {color: string; size?: number}): React.JSX.Element {
  const anims = useRef([0, 1, 2].map(() => new Animated.Value(0))).current;
  useEffect(() => {
    const loop = Animated.loop(
      Animated.stagger(
        140,
        anims.map(a =>
          Animated.sequence([
            Animated.timing(a, {toValue: 1, duration: 220, useNativeDriver: true}),
            Animated.timing(a, {toValue: 0, duration: 220, useNativeDriver: true}),
          ]),
        ),
      ),
    );
    loop.start();
    return () => loop.stop();
  }, [anims]);
  return (
    <View style={styles.row}>
      {anims.map((a, i) => (
        <Animated.View
          key={i}
          style={{
            width: size,
            height: size,
            backgroundColor: color,
            opacity: a.interpolate({inputRange: [0, 1], outputRange: [0.35, 1]}),
            transform: [{translateY: a.interpolate({inputRange: [0, 1], outputRange: [0, -size * 0.6]})}],
          }}
        />
      ))}
    </View>
  );
}

const styles = StyleSheet.create({
  row: {flexDirection: 'row', alignItems: 'flex-end', gap: 3, height: 10},
});
