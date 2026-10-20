// Wraps react-native-web for the "react-native" -> ??? alias in
// vite.config.ts. react-native-svg imports TurboModuleRegistry directly
// from 'react-native' (not a subpath we could alias on its own — see
// turboModuleRegistryWebStub.ts for why that stub couldn't be wired in via
// aliasing the two relative-import files that use it: Vite/Rollup aliases
// never intercept relative specifiers, only bare ones). react-native-web has
// no TurboModuleRegistry export at all, so re-export everything from it and
// add a stub TurboModuleRegistry here instead.
export * from 'react-native-web';

export const TurboModuleRegistry = {
  getEnforcing(name: string): unknown {
    return new Proxy(
      {},
      {
        get(_target, prop) {
          return () => {
            throw new Error(`TurboModule "${name}.${String(prop)}" is not available on web`);
          };
        },
      },
    );
  },
  get(): null {
    return null;
  },
};
