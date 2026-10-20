import React from 'react';
import {Pressable, ScrollView, StyleSheet, Text, View} from 'react-native';

/**
 * Catches an error anywhere in the app's screens and shows it, instead of
 * the whole app closing with no word of what happened. "Try again" re-mounts
 * everything (keys and rooms stay in memory, so it's quick).
 */
export class CrashScreen extends React.Component<{children: React.ReactNode}, {error: Error | null; stack: string}> {
  state = {error: null as Error | null, stack: ''};

  static getDerivedStateFromError(error: Error) {
    return {error};
  }

  componentDidCatch(error: Error, info: React.ErrorInfo): void {
    console.error('[supermatrix] crash', error, info.componentStack);
    this.setState({stack: (info.componentStack ?? '').trim().split('\n').slice(0, 6).join('\n')});
  }

  render(): React.ReactNode {
    const {error, stack} = this.state;
    if (!error) return this.props.children;
    return (
      <View style={styles.page}>
        <ScrollView contentContainerStyle={styles.content}>
          <Text style={styles.title}>Something broke</Text>
          <Text style={styles.body}>Super Matrix hit an error. Your deployments are safe on the server.</Text>
          <Text style={styles.code} selectable>
            {error.name}: {error.message}
            {stack ? `\n\n${stack}` : ''}
          </Text>
          <Pressable style={styles.button} onPress={() => this.setState({error: null, stack: ''})}>
            <Text style={styles.buttonText}>Try again</Text>
          </Pressable>
        </ScrollView>
      </View>
    );
  }
}

// Fixed colors: the theme may be what broke.
const styles = StyleSheet.create({
  page: {flex: 1, backgroundColor: '#0d1117'},
  content: {padding: 24, paddingTop: 64, gap: 14},
  title: {color: '#f0f6fc', fontSize: 20, fontWeight: '800'},
  body: {color: '#c9d1d9', fontSize: 14},
  code: {color: '#ff9b9b', fontSize: 12, fontFamily: 'monospace', backgroundColor: '#161b22', padding: 12, borderRadius: 8},
  button: {backgroundColor: '#2f81f7', borderRadius: 8, paddingVertical: 12, alignItems: 'center'},
  buttonText: {color: '#fff', fontWeight: '700'},
});
