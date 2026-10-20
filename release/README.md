# release/

Local signing material and the guided release tool for the Android app.
Mirrors `../horus-33/release/`.

Tracked in git: `release.sh`, `gen-keystore.sh`, `README.md`, `.gitignore`.
**Never** committed: `release.keystore`, `keystore.env`, or any built `.apk`.

## First-time setup

```
./release/gen-keystore.sh
```

Generates `release.keystore` (a real Android signing key, valid ~27 years)
and `keystore.env` (its passwords). Refuses to run again once these exist —
regenerating would produce a *different* key, and Android won't install an
update signed by a different key over an app installed from the old one, so
losing/replacing this keystore means every existing install of the app can
never again be updated in place (only uninstalled and reinstalled fresh).
**Back up `release.keystore` and `keystore.env` somewhere durable outside
this checkout.**

## Cutting a release

```
./release/release.sh
```

(or `npm run release` from `app/`, which just calls this)

The script: checks the toolchain, sources `keystore.env`, lists existing
`app-v*` tags, prompts for the new tag (e.g. `app-v0.3.2`) and a
`versionCode`, runs the real `./gradlew assembleSideloadRelease` with full output,
then — after a single confirmation — creates and pushes the tag and publishes
a GitHub Release with the signed APK attached (named `supermatrix-<tag>.apk`,
matching `app/src/appUpdater.ts`'s `APK_NAME` regex so the in-app updater
actually finds it).

Assumes `git`, `java`, the Android SDK/build-tools, and `gh` (authenticated)
are installed (the Android SDK system-wide; the flake leaves it out on purpose).

The signed APK is also left at `release/supermatrix-<tag>.apk` (gitignored).

`release.sh` keeps the last versionCode built in `sideload-version.env`
(gitignored), suggests the next one above it, and refuses anything lower.
