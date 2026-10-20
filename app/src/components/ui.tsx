// The small form pieces every screen uses, styled like super-chat's
// Settings screen so the apps look alike.
import React, {useState} from 'react';
import {
  ActivityIndicator,
  Clipboard,
  Pressable,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  TextInputProps,
  View,
  ViewStyle,
} from 'react-native';

import {Theme, useTheme} from '../theme';
import {Icon, IconName} from './Icon';

export function Title({children}: {children: React.ReactNode}) {
  const s = makeStyles(useTheme());
  return <Text style={s.title}>{children}</Text>;
}

export function Label({children, spaced}: {children: React.ReactNode; spaced?: boolean}) {
  const s = makeStyles(useTheme());
  return <Text style={[s.label, spaced && s.spaced]}>{children}</Text>;
}

export function Hint({children, error}: {children: React.ReactNode; error?: boolean}) {
  const s = makeStyles(useTheme());
  return <Text style={[s.hint, error && s.hintError]}>{children}</Text>;
}

export function Divider() {
  const s = makeStyles(useTheme());
  return <View style={s.divider} />;
}

export function Input(props: TextInputProps & {mono?: boolean}) {
  const theme = useTheme();
  const s = makeStyles(theme);
  const {mono, style, ...rest} = props;
  return (
    <TextInput
      placeholderTextColor={theme.textMuted}
      autoCapitalize="none"
      autoCorrect={false}
      {...rest}
      style={[s.input, mono && s.mono, style]}
    />
  );
}

export function Button({
  label,
  onPress,
  busy,
  disabled,
  kind = 'primary',
  icon,
  style,
}: {
  label: string;
  onPress: () => void;
  busy?: boolean;
  disabled?: boolean;
  kind?: 'primary' | 'secondary' | 'danger';
  icon?: IconName;
  style?: ViewStyle;
}) {
  const theme = useTheme();
  const s = makeStyles(theme);
  const color = kind === 'primary' ? theme.primaryText : kind === 'danger' ? theme.danger : theme.text;
  return (
    <Pressable
      style={({pressed}) => [
        kind === 'primary' ? s.button : kind === 'danger' ? s.dangerButton : s.secondary,
        (disabled || busy) && s.disabled,
        pressed && s.pressed,
        style,
      ]}
      onPress={onPress}
      disabled={disabled || busy}
      accessibilityRole="button"
      accessibilityLabel={label}>
      {busy ? (
        <ActivityIndicator size="small" color={color} />
      ) : (
        <View style={s.buttonInner}>
          {icon ? <Icon name={icon} size={16} color={color} /> : null}
          <Text style={[s.buttonText, {color}]}>{label}</Text>
        </View>
      )}
    </Pressable>
  );
}

/** One of a few options, as a row of chips. */
export function Chips<T extends string | number>({
  options,
  value,
  onChange,
}: {
  options: {value: T; label: string}[];
  value: T;
  onChange: (v: T) => void;
}) {
  const s = makeStyles(useTheme());
  return (
    <View style={s.chips}>
      {options.map(o => (
        <Pressable
          key={String(o.value)}
          style={[s.chip, value === o.value && s.chipOn]}
          onPress={() => onChange(o.value)}
          accessibilityRole="radio"
          accessibilityState={{selected: value === o.value}}>
          <Text style={[s.chipText, value === o.value && s.chipOnText]}>{o.label}</Text>
        </Pressable>
      ))}
    </View>
  );
}

export function ToggleRow({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint?: string;
  value: boolean;
  onChange: (v: boolean) => void;
}) {
  const s = makeStyles(useTheme());
  return (
    <View style={s.rowBetween}>
      <View style={s.rowText}>
        <Text style={s.toggleLabel}>{label}</Text>
        {hint ? <Text style={s.hint}>{hint}</Text> : null}
      </View>
      <Switch value={value} onValueChange={onChange} accessibilityLabel={label} />
    </View>
  );
}

/** A secret or snippet shown in full, selectable, with a copy button. */
export function CopyBox({text, label}: {text: string; label?: string}) {
  const theme = useTheme();
  const s = makeStyles(theme);
  const [copied, setCopied] = useState(false);
  return (
    <View style={s.copyBox}>
      <Text style={s.copyText} selectable>
        {text}
      </Text>
      <Pressable
        style={s.copyButton}
        onPress={() => {
          Clipboard.setString(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        }}
        accessibilityLabel={label ? `Copy ${label}` : 'Copy'}>
        <Icon name={copied ? 'check' : 'copy'} size={16} color={copied ? theme.success : theme.textMuted} />
      </Pressable>
    </View>
  );
}

/** A colored status dot. */
export function Dot({color}: {color: string}) {
  return <View style={[dotStyle.dot, {backgroundColor: color}]} />;
}

const dotStyle = StyleSheet.create({dot: {width: 8, height: 8, borderRadius: 4}});

export function makeStyles(theme: Theme) {
  return StyleSheet.create({
    title: {fontSize: 15, fontWeight: '700', marginBottom: 10, color: theme.text},
    label: {fontSize: 13, fontWeight: '600', marginBottom: 6, color: theme.textMuted},
    spaced: {marginTop: 14},
    hint: {fontSize: 12, color: theme.textMuted, marginTop: 6, lineHeight: 17},
    hintError: {color: theme.danger},
    divider: {height: 1, backgroundColor: theme.border, marginVertical: 24},
    input: {
      borderWidth: 1,
      borderColor: theme.border,
      borderRadius: 6,
      paddingHorizontal: 10,
      paddingVertical: 8,
      fontSize: 15,
      color: theme.text,
      backgroundColor: theme.surface,
    },
    mono: {fontFamily: 'monospace', fontSize: 14},
    button: {backgroundColor: theme.primary, borderRadius: 8, paddingVertical: 12, paddingHorizontal: 16, alignItems: 'center'},
    secondary: {
      alignSelf: 'flex-start',
      paddingHorizontal: 14,
      paddingVertical: 8,
      borderRadius: 6,
      backgroundColor: theme.surfaceAlt,
    },
    dangerButton: {paddingVertical: 12, borderRadius: 8, backgroundColor: theme.dangerBg, alignItems: 'center'},
    buttonInner: {flexDirection: 'row', alignItems: 'center', gap: 6},
    buttonText: {fontWeight: '600', fontSize: 14},
    disabled: {opacity: 0.5},
    pressed: {opacity: 0.75},
    chips: {flexDirection: 'row', flexWrap: 'wrap', gap: 8},
    chip: {paddingHorizontal: 14, paddingVertical: 8, borderRadius: 6, backgroundColor: theme.surfaceAlt},
    chipOn: {backgroundColor: theme.primary},
    chipText: {fontWeight: '600', fontSize: 13, color: theme.text},
    chipOnText: {color: theme.primaryText},
    rowBetween: {flexDirection: 'row', alignItems: 'center', gap: 12, marginVertical: 8},
    rowText: {flex: 1, minWidth: 0},
    toggleLabel: {fontSize: 14, fontWeight: '600', color: theme.text},
    copyBox: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      gap: 8,
      padding: 10,
      borderRadius: 6,
      borderWidth: 1,
      borderColor: theme.border,
      backgroundColor: theme.codeBg,
      marginTop: 8,
    },
    copyText: {flex: 1, minWidth: 0, fontFamily: 'monospace', fontSize: 12, lineHeight: 18, color: theme.codeText},
    copyButton: {padding: 4},
  });
}
