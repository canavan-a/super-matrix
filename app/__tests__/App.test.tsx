/**
 * @format
 */

import 'react-native';
import React from 'react';
import App from '../src/App';

// Note: import explicitly to use the types shipped with jest.
import {expect, it, jest} from '@jest/globals';

// Note: test renderer must be required after react-native.
import renderer, {act} from 'react-test-renderer';

// The launch splash has its own tests (splash.test.tsx); here it is stubbed so
// this stays a test of the app, and no animation timers outlive it.
jest.mock('../src/logo/SplashGate', () => ({
  SplashGate: ({children}: {children: React.ReactNode}) => children,
}));

it('opens on the sign-in screen with no saved session', async () => {
  let tree: renderer.ReactTestRenderer | undefined;
  await act(async () => {
    tree = renderer.create(<App />);
  });
  const labels = tree!.root.findAll(n => typeof n.props?.accessibilityLabel === 'string').map(n => n.props.accessibilityLabel);
  expect(labels).toContain('Server address');
  expect(labels).toContain('Root password');
  expect(labels).toContain('Sign in');
  expect(labels).toContain('Super Matrix logo');
  await act(async () => {
    tree?.unmount();
  });
});
