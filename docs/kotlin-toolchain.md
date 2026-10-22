# Kotlin Toolchain builds

A full-integration project can use the Kotlin Toolchain instead of Gradle or direct Xcode
builds. Configure each target in `.maestro/mockstro.yaml`:

```yaml
platforms:
  android:
    appId: dev.kairos.android
    integration: full
    build:
      system: kotlin-toolchain
      module: android
      variant: debug
  ios:
    appId: dev.kairos.ios
    integration: full
    build:
      system: kotlin-toolchain
      module: ios
      variant: debug
```

Run both configured platforms together, or select one with `--platform ios` or
`--platform android`. Each target's `appId` identifies its installed app; shared flows use
`appId: ${APP_ID}`. The build variant is `debug` (the default) or `release`;
Gradle task and Xcode project/scheme settings are not required.

Mockstro runs the project wrapper from each target's source checkout (`platforms.<name>.project`,
or the suite repository when omitted):

```sh
./kotlin build -m android -p android -v debug
./kotlin build -m ios -p iosSimulatorArm64 -v debug
```

Device selection, installation, Android port forwarding, override acceptance, cleanup and
parallel scheduling are unchanged. Parallel workers share one snapshotted build, not a
mutable toolchain output. Use `--skip-build` when an appropriate test build is installed.

The defaults follow Kotlin Toolchain 0.13's output layout:

```text
build/tasks/_<android-module>_buildAndroid<Variant>/*.apk
build/tasks/_<ios-module>_buildIosAppIosSimulatorArm64<Variant>/bin/<Variant>-iphonesimulator/*.app
```

Override these paths with each target's `build.artifact` if the layout changes. Relative
paths resolve against the target's source checkout. Exactly one matching artifact is required; ambiguous
outputs fail instead of choosing a stale or unintended build.

Android APKs are copied and hashed. iOS simulator bundles are checked for their application
ID, platform and actual `CFBundleExecutable`, copied in full with framework symlinks and file
modes preserved, and hashed with resources included. Bundle symlinks must be relative
and resolve within the bundle so the snapshot has no external mutable dependencies. Binary Info.plist files are read via
macOS `plutil`. Hash/read errors fail the build instead of returning an unverified artifact.

When `build.system` is omitted, the defaults are `gradle` on Android and `xcode` on
iOS. A lite project uses the app already installed and does not build.

For a different toolchain layout or a project-owned task, use the generic command builder:

```yaml
build:
  command: [mise, run, build-ios]
  artifact: out/ios/*.app
```

`command` is an argv list executed in the source checkout, with no implicit shell.
`system: command` may be specified explicitly. A command must include an artifact glob.
A failed Kotlin, Gradle or Xcode build returns its error; Mockstro never silently retries
through a custom command. See [configuration](configuration.md#builds) for Make and other
command examples.

For WebSockets, also configure the pinned beta and endpoint described in [websockets.md](websockets.md).
