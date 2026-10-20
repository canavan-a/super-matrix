# Super Matrix app

Bare React Native 0.81 (Android) plus a Vite/react-native-web build from the
same code, forked from super-chat's app (itself built on super-badger's): the
same header, themes, splash, themed launcher icons and sideload updater.

```sh
npm ci
npm run web            # browser client on :5173 (or build it and let the server serve it)
npm run android        # on a device/emulator (system Android SDK)
npm test               # unit tests (logo, splash, icon, forms)
E2E=1 npx jest __tests__/e2e.test.ts --forceExit   # builds and runs the real server and relay
```

## Layout

- `src/store.ts`: the open server's session, its relays and deployments, and
  live relay status over one WebSocket (`/v1/ws`). Every action (sign in, add a
  relay, save a deployment…) is here and refetches afterwards.
- `src/api.ts`: the admin API, with the session token as a bearer token.
- `src/keystore.ts` / `keystore.web.ts`: where sessions are kept (the Android
  Keystore, or localStorage on the web). The root password is never stored.
- `src/screens/`: sign-in, deployments (home), deployment editor, relays,
  settings.

## Security notes

- `MainActivity.kt` sets `FLAG_SECURE` (no screenshots, screen recording or
  recents thumbnail): the app shows relay and API tokens. React Native's
  `<Modal>` opens its own window without that flag, so sheets go through
  `components/Overlay.tsx` instead.
- Tokens are shown once, when made; the server keeps only hashes.

# Launch splash and icons

The splash is super-badger's animation with the "Super Matrix" lettering: the
letters rise one by one, then a shine sweeps across. Tap to skip; it honors
"reduce motion", and turns itself off if it fails to finish twice in a row.
`/splash-preview.html?theme=ember` on the web build shows it on its own.

The launcher icon is broomledger's broom (six squares on a 3×3 grid), one per
theme, switched as the app goes to the background (Settings → "Match app icon
to theme"). The sign-in screen shows the broom on its tile over the stacked
wordmark.

**The art is generated, not drawn here.** Edit `art/mark.txt` (the broom) or
`art/word.txt` (the wordmark; "Super", "a", "t" and "r" come from super-chat's),
then:

    cd tools && go run ./appart              # -> src/logo/logoData.ts, iconPalette.json, launcher icons
    cd tools && go run ./appart -preview /tmp/out   # also PNG previews per theme

Theme colors are read from `src/theme.tsx`.
