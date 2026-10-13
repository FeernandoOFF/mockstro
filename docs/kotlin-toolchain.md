# Kotlin Toolchain builds

A full-integration project can use the Kotlin Toolchain instead of Gradle or direct Xcode
builds. Set these values in `.maestro/project.env`:

```dotenv
BUILD_SYSTEM=kotlin-toolchain
KOTLIN_ANDROID_MODULE=android
KOTLIN_IOS_MODULE=ios
KOTLIN_VARIANT=debug
APP_PACKAGE=dev.kairos.android
```

For a project containing both apps, add `IOS_APP_PACKAGE=dev.kairos.ios` and select iOS
with `--platform ios`. For an iOS-only project, use `MAESTRO_PLATFORM=ios` and
`APP_PACKAGE=dev.kairos.ios`. `APP_PACKAGE` remains the
installed application ID on either platform. The build variant is `debug` or `release`;
Gradle's `MAESTRO_APP_VARIANT`, task and Xcode project/scheme settings are not required.

Mockstro runs the project wrapper from the repository root:

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

Override these paths with `ANDROID_APK_GLOB` and `IOS_APP_GLOB` if the layout changes. Relative
paths resolve against the project root. Exactly one matching artifact is required; ambiguous
outputs fail instead of choosing a stale or unintended build.

Android APKs are copied and hashed. iOS simulator bundles are checked for their application
ID, platform and actual `CFBundleExecutable`, copied in full with framework symlinks and file
modes preserved, and hashed with resources included. Binary Info.plist files are read via
macOS `plutil`. Hash/read errors fail the build instead of returning an unverified artifact.

`BUILD_SYSTEM=native` is the default for existing projects: Gradle on Android and Xcode on
iOS. A lite project still uses the app already installed and does not build.

For WebSockets, also configure the pinned beta and endpoint described in [websockets.md](websockets.md).
