// AsyncStorage is a native module; under Jest it needs the official in-memory
// mock, or anything that imports src/settings.ts fails with "AsyncStorage is null".
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'),
);

// The Keystore (src/keystore.ts): an in-memory stand-in.
jest.mock('react-native-keychain', () => {
  const store = new Map();
  return {
    ACCESS_CONTROL: {}, ACCESSIBLE: {}, STORAGE_TYPE: {}, SECURITY_LEVEL: {},
    getAllGenericPasswordServices: jest.fn(async () => Array.from(store.keys())),
    setGenericPassword: jest.fn(async (username, password, opts) => {
      store.set(opts?.service, {username, password});
      return true;
    }),
    getGenericPassword: jest.fn(async opts => store.get(opts?.service) ?? false),
    resetGenericPassword: jest.fn(async opts => store.delete(opts?.service)),
  };
});
// Safe-area insets (src/App.tsx): the library's own Jest mock.
jest.mock('react-native-safe-area-context', () => require('react-native-safe-area-context/jest/mock').default);
