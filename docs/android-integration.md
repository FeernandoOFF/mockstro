# Android: recommended full integration

How to add the runtime-override seam ([integration.md](integration.md), "Full") to an Android app
so that mockstro can point each run at its own WireMock, prove the app honoured it, and run devices in
parallel, without any of it being able to reach a production build.

## Principles

1. **The seam lives only in debug source sets.** Production code must not contain the override
   parser, the Settings reads/writes or the test-only Remote Config. Guard by what is compiled,
   not by runtime checks such as `BUILD_TYPE == "release"` or "the app id contains `test`".
2. **`main` depends on an API, not on the seam.** `main` reads the effective configuration
   through one object or interface; each source set supplies its own implementation.
3. **Configuration is runtime, per process.** Compile-time values (`BuildConfig`) stay the
   defaults; the override replaces them for one run only and expires on its own.
4. **The app proves what it honoured.** mockstro never assumes a written setting was read.
5. **E2E is deterministic.** No values from Google, the disk or a previous run leak into a flow.

## Source sets

Put every non-shipping build type in one shared source set (e.g. `src/debuggable`) and give the
shipping ones a stub source set (e.g. `src/release`) with the same class names:

```kotlin
// app/build.gradle.kts
val debuggableBuildTypes = listOf("debug", "qa", "debugQa")
android {
    sourceSets {
        debuggableBuildTypes.forEach { getByName(it) {
            java.srcDir("src/debuggable/java")
            manifest.srcFile("src/debuggable/AndroidManifest.xml")
            res.srcDir("src/debuggable/res")
        } }
        // A build type that ships (store, internal sharing) uses the release stubs
        getByName("qaRelease") { java.srcDir("src/release/java") }
    }
}
```

- A class with the same fully qualified name in both source sets is swapped at compile time. The
  release version is plain constants and no-ops, so R8 removes the call sites' dead branches.
- Any build type that is distributed outside the team (non-debuggable, production app id,
  internal app sharing) belongs with release, even if it points at a test backend.
- Debug-only dependencies (debug menus, inspectors) are added per debuggable build type only.

## One runtime environment object

Replace scattered debug flags with a single object that `main` reads:

| Member | Debuggable | Release |
|---|---|---|
| `apiBaseUrl` | override's URL, else `BuildConfig.BASE_URL` | `BuildConfig.BASE_URL` |
| launch-argument flags (skip login, disable autofill, …) | parsed from the launch intent | `false` |
| `configureFrom(intent)` | reads extras | no-op |

Every HTTP client builder takes `apiBaseUrl` from it, and so does anything that derives values from
the base URL (request signing, URL rewriting, deep-link hosts). One missed `BuildConfig.BASE_URL`
means that path still talks to the real backend.

## The override channel

Use **`Settings.Global`**, written by mockstro with `adb shell settings put global <key> <base64 JSON>`:

- it survives `pm clear`, which every cold flow performs (app storage does not);
- the shell user can write it without root, and any app can read it;
- base64 keeps the value intact across the host and device shells.

Launch-intent extras are the wrong channel: they are lost on process restart and deep-link
launches, and arrive after the HTTP clients may already exist. A file in `/data/local/tmp` is
blocked by SELinux on recent Android versions.

Payload (`schema` 1): `applicationId`, `runId`, `expiresAt` (epoch ms), `apiUrl`, `remoteConfig`
(string→string). Honour it only if **all** hold, otherwise ignore it and log why:

- schema matches and `applicationId` is this app's;
- `expiresAt` is in the future, so a crashed run leaves the app working normally;
- `apiUrl` is plain `http` to loopback (`127.0.0.1`, `localhost`, `10.0.2.2`) with the expected
  path, no user info, query or fragment, so a bad value cannot redirect traffic elsewhere;
- `runId` matches a conservative charset and the payload is size-bounded.

## When to read it: a debug-only ContentProvider

Read the override once per process, before anything builds an HTTP client. Declare a
`ContentProvider` in the **debuggable manifest only**; Android creates providers after
`Application.attachBaseContext` and before `Application.onCreate`, and DI graphs are built after
that. The `Application` class needs no changes.

```xml
<!-- src/debuggable/AndroidManifest.xml -->
<uses-permission android:name="android.permission.WRITE_SECURE_SETTINGS"
    tools:ignore="ProtectedPermissions" />
<application>
    <provider android:name="com.example.config.E2eInitProvider"
        android:authorities="${applicationId}.e2e-init" android:exported="false" />
</application>
```

```kotlin
class E2eInitProvider : ContentProvider() {
    override fun onCreate(): Boolean {
        AppEnvironment.install(requireNotNull(context)) // read, validate, acknowledge
        return true
    }
    // query/insert/update/delete/getType: return null / 0
}
```

Log with `android.util.Log` here: logging libraries are usually initialised later in
`Application.onCreate`, so their output from the provider is lost, including the reason an
override was rejected.

## Acknowledge in the same place

Right after a payload is accepted, write its `runId` to the acceptance key
(`E2E_ACCEPT_SETTING_KEY`) in `Settings.Global`. mockstro:

1. grants `WRITE_SECURE_SETTINGS` after every install (development permissions do not survive a
   reinstall; the manifest must declare it or the grant fails);
2. deletes the acceptance key before each launch, so a stale value cannot count;
3. fails the flow with `OVERRIDE` if, after the flow, the key does not hold this run's id.

This catches an old build without the seam, a rejected payload (expired, wrong app id) and a
process that started before the override was written.

It proves the process *read* the override, not that the flow's traffic reached the mock; the
unmatched-request report and the flow's own assertions cover that. Proving acceptance from the
mock's request log instead (a run-id header on every API request) was tried and dropped: a flow
that finishes before the app sends its first request fails even though nothing is wrong with the
seam.

## Networking

- Debug `network_security_config`: permit cleartext to loopback (and keep user CAs trusted if you
  also want lite/Proxyman). Release keeps the platform defaults.
- mockstro runs `adb reverse tcp:<port>`, so `127.0.0.1` on the device is the Mac. Each run has its
  own port, which is what makes parallel devices possible.
- A device-wide HTTP proxy overrides all of this; mockstro refuses such a device unless told to clear it.

## Remote Config

Route **all** Remote Config access through one app interface, and bind it per source set with DI:

- **release**: the real Firebase-backed implementation, unchanged;
- **debuggable, override active**: a local implementation that serves the bundled defaults XML
  with the run's `remoteConfig` on top, and never touches Firebase;
- **debuggable, no override**: the real implementation (optionally wrapped by debug tooling such
  as "show keys instead of values").

Why not seed Firebase itself: it needs a reset of previously activated values, awaits on the main
thread during startup (a timeout means a crash), keeps state on disk between launches, and still
races the app's own fetches. The local implementation is instant, identical on every launch and
isolated per process, so parallel runs cannot interfere.

Match Firebase's semantics so E2E behaves like production: a missing key reads as `""`, `0` or
`false`; booleans accept `1`, `true`, `t`, `yes`, `y`, `on` (case-insensitive); unparsable numbers
read as `0`. `initialize()` and `fetchAndActivate()` become no-ops.

## Designated emulators

Agents and CI only drive designated emulators. Set `platforms.android.devices` in
`.maestro/mockstro.yaml`, e.g. `[Resizable_Android_17, Resizable_Android_16]`. Use
`.maestro/mockstro.local.yaml` for machine-specific overrides or `~/.config/mockstro/designated.txt`. Humans may use any emulator; phones use `--map-remote` (lite).

## Writing flows against the seam

Use `appId: ${APP_ID}` in flow headers. Keep per-platform IDs in `mockstro.yaml`; native
Maestro `when: { platform: Android }` or `when: { platform: iOS }` conditions select subflows.

- **Wait for data, not chrome.** A "screen loaded" helper must wait for an element that only
  renders after the API responds (a dedicated test tag on the loaded state works well), not for
  a toolbar or navigation item that appears immediately.
- **Assert something positive before asserting absence.** `assertNotVisible` passes on an empty
  screen, so a flow made only of negative checks can pass before the app has loaded anything.
- Launch arguments live in memory: relaunch through a shared flow that passes them again.

## Verifying the integration

- `mockstro run` one flow: logcat shows the override accepted with this run's id, and the result is
  `PASS` rather than `OVERRIDE`.
- Break it on purpose: an expired or foreign payload is rejected with a logged reason and the flow
  fails with `OVERRIDE`.
- Check the release build: it compiles against the stubs, its merged manifest has neither the
  provider nor `WRITE_SECURE_SETTINGS`, and its dex contains none of the seam's classes.
