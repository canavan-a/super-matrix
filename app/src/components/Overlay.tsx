import React, {useEffect} from 'react';
import {BackHandler, Pressable, StyleSheet, View, ViewStyle} from 'react-native';

import {Portal} from './Portal';

/**
 * A modal layer drawn inside the app's own window. React Native's <Modal>
 * opens a separate Android window that doesn't inherit MainActivity's
 * FLAG_SECURE, so anything shown in one could be screenshotted. Every sheet
 * or viewer that can show message content uses this instead.
 */
export function Overlay({
  visible,
  onClose,
  backdrop = 'rgba(0,0,0,0.35)',
  style,
  modal,
  children,
}: {
  visible: boolean;
  onClose: () => void;
  backdrop?: string;
  /** Layout of the content area (e.g. justifyContent: 'flex-end'). */
  style?: ViewStyle;
  /**
   * Cover the whole app (drawn at its root through a Portal), not just the
   * container it's written in: for sheets opened from inside a scrolling
   * page or a list row.
   */
  modal?: boolean;
  children: React.ReactNode;
}): React.JSX.Element | null {
  useEffect(() => {
    if (!visible) return;
    const sub = BackHandler.addEventListener('hardwareBackPress', () => {
      onClose();
      return true;
    });
    return () => sub.remove();
  }, [visible, onClose]);
  if (!visible) return null;
  const layer = (
    <View style={[StyleSheet.absoluteFill, styles.layer]}>
      <Pressable style={[StyleSheet.absoluteFill, {backgroundColor: backdrop}]} onPress={onClose} />
      <View style={[styles.content, style]} pointerEvents="box-none">
        {children}
      </View>
    </View>
  );
  return modal ? <Portal>{layer}</Portal> : layer;
}

const styles = StyleSheet.create({
  layer: {zIndex: 50, elevation: 50},
  content: {flex: 1},
});
