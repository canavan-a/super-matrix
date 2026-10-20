import path from 'path';

import {defineConfig} from 'vite';
import react from '@vitejs/plugin-react';

import {pwa} from './pwa/vite-plugin';

// Aliases "react-native" -> "react-native-web" so the exact same src/App.tsx
// used by Metro (native/Android) renders in a browser here, with real Vite
// dev-server HMR — see app/README.md. The server URL/auth token are runtime
// settings (src/settings.ts, editable from the in-app Settings screen), not
// build-time config, so there's nothing to inject here.
export default defineConfig(({mode}) => ({
  plugins: [react({include: /\.(jsx|js|tsx|ts)$/}), pwa()],
  resolve: {
    alias: {
      // More specific "react-native/..." aliases must precede the generic
      // "react-native" -> "react-native-web" one below — Vite matches alias
      // keys in order, and the generic prefix would otherwise win first.
      //
      // react-native-svg ships a real, DOM-based web implementation
      // (lib/module/elements.web.js's WebShape class) specifically so it
      // doesn't need the native Fabric bridge at all on web — but its
      // package.json "module"/"main" fields point at the extensionless
      // native entry (lib/module/index.js -> ReactNativeSVG.js -> ./fabric),
      // and Vite's dep-optimizer resolves that exact literal path rather
      // than retrying it against the `.web.js`-first `extensions` list
      // below. Left alone, that pulls in Fabric's codegenNativeComponent
      // native-component files, which encode colors/enums as native-only
      // values (processColor ints, enum ints) that don't mean anything as
      // DOM SVG attributes — every shape rendered empty/invisible with
      // console warnings like `stroke="[object Object]"`.
      //
      // Aliased straight to elements.web.js (just the shape components:
      // Svg/Path/Circle/Line/Rect/...), not the package's own
      // ReactNativeSVG.web.js barrel — that barrel also re-exports
      // SvgUri/SvgXml's remote-fetch helpers, which pull in a `buffer`
      // polyfill that doesn't resolve cleanly under Vite and crashes the
      // whole page. Nothing this app renders (DataPointChart, Icon) needs
      // those, only plain shape primitives.
      //
      // This alias is a prefix match (see the note below), so any code
      // elsewhere still doing a deep import like
      // `react-native-svg/lib/module/fabric/...` would get mangled into
      // `<this path>/lib/module/fabric/...` — codegenNativeComponentWebStub
      // below stays in place as the defensive fallback for exactly that.
      'react-native-svg': path.resolve(__dirname, 'node_modules/react-native-svg/lib/module/elements.web.js'),
      // See codegenNativeComponentWebStub.ts — react-native-svg's Fabric-only
      // native-component files pull this in even on the web build, but it
      // has no react-native-web equivalent to alias to. Kept as a fallback
      // even with the react-native-svg alias above, since Vite's dep-
      // optimizer scan can still reach these files by their own static
      // imports independent of which entry point application code actually
      // uses.
      'react-native/Libraries/Utilities/codegenNativeComponent': path.resolve(__dirname, 'src/stubs/codegenNativeComponentWebStub.ts'),
      // See reactNativeWebShim.ts — react-native-svg imports TurboModuleRegistry
      // directly from "react-native" (a named export react-native-web doesn't
      // have), via files that reach it through relative imports Vite's alias
      // can't intercept on their own, so the shim patches it in at this level
      // instead of aliasing straight to "react-native-web".
      'react-native': path.resolve(__dirname, 'src/stubs/reactNativeWebShim.ts'),
      // @notifee/react-native is native-only (background notifications,
      // Android-only, always behind a dynamic import()). Its real package
      // requires a bare RN internal path that doesn't exist under
      // react-native-web — harmless in the production build (Rollup tree-
      // shakes the whole Android-only branch away since it's never
      // reachable), but Vite's dev-server dependency prescan crawls it
      // eagerly regardless of reachability and crashes outright. Aliasing
      // to a stub sidesteps that scan entirely — see the stub's own comment
      // for the exact failure this fixes.
      '@notifee/react-native': path.resolve(__dirname, 'src/stubs/notifeeWebStub.ts'),
      // See assetsRegistryWebStub.ts — react-native-svg's asset-URI resolver
      // pulls in this Flow-syntax-only native package even on its web build.
      '@react-native/assets-registry/registry': path.resolve(__dirname, 'src/stubs/assetsRegistryWebStub.ts'),
    },
    extensions: [
      '.web.tsx',
      '.web.ts',
      '.web.jsx',
      '.web.js',
      '.tsx',
      '.ts',
      '.jsx',
      '.js',
    ],
  },
  define: {
    // react-native-web checks this at runtime.
    'process.env.NODE_ENV': JSON.stringify(mode === 'production' ? 'production' : 'development'),
    // React Native's `global`, which react-native-web's Animated still calls
    // (global.cancelAnimationFrame when an animation stops). Without it,
    // stopping the typing dots threw and blanked the page.
    global: 'globalThis',
  },
}));
