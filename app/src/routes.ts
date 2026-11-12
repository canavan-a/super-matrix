// Hand-rolled routing: react-navigation drags in native-only deps
// (react-native-screens/gesture-handler/reanimated) that complicate the
// react-native-web/Vite target for a handful of screens, so App.tsx just
// switches on this union instead.
export type Route =
  | {name: 'home'}
  | {name: 'deployment'; id: string | null}
  | {name: 'bridge'; id: string | null}
  | {name: 'relays'}
  | {name: 'domains'}
  | {name: 'groups'}
  | {name: 'settings'};
