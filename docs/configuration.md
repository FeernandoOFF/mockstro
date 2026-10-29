# Configuration

Everything lives in `.maestro/mockstro.yaml` (committed) and the optional
`.maestro/mockstro.local.yaml` (ignored, per machine).

The [installer](../README.md#quickstart) creates `mockstro.yaml` when it's missing, by
running `init` with any flags you pass:

```bash
sh install.sh v0.1.0-rc.3 --android-app-id com.example.android --ios-app-id com.example.ios \
  --android-project ../android-app --ios-project ../ios-app --integration full
```

This creates a config, a starter smoke flow, and ignores `mockstro.local.yaml` and
`.runtime/`. Existing `.maestro` files are kept.

## mockstro.yaml

```yaml
cliVersion: v0.1.0-rc.3          # release used by ./mockstrow
env:
  TEST_USER: demo
platforms:
  android:
    project: ../android-app      # source checkout, relative to this repo
    appId: com.example.android
    integration: full            # lite (default) or full
    devices: [Agent_Pixel]       # emulators agents may use
    build:
      task: assembleDebug
      artifact: app/build/outputs/apk/debug/*.apk
  ios:
    project: ../ios-app
    appId: com.example.ios
    integration: full
    devices: [Agent_iPhone]
    build:
      project: App.xcworkspace
      scheme: App
      configuration: Debug
```

| Key | Notes |
| --- | --- |
| `appId` | required, literal |
| `integration` | `lite` or `full`, see [integration](integration.md) |
| `devices` | emulator/simulator names agents may use, see [devices](devices.md) |
| `build` | Select Gradle, Xcode, Kotlin Toolchain or an explicit task command; see below |
| `wiremockVersion` | Legacy setting; omit it. The CLI pins WireMock `4.0.0-beta.41` for HTTP and [WebSockets](websockets.md). Old `3.13.2` / `4.0.0-beta.40` settings are accepted and use the CLI pin. |
| `proxyBaseUrl`, `fallbackUrl`, `fbrcUrl`, `overrideKey`, `acceptKey` | advanced |

Unknown keys print a warning and are ignored, so newer configs still load.

## Web catalog metadata

The web library works without metadata: each profile name becomes a group, scenarios and
flows use their profile's group, and items without a profile appear under **Other**.
All scenario and flow header `tags` appear as filter chips.

Optional top-level `catalog` settings in the shared `mockstro.yaml` customize this for
the whole suite, including every platform and locally added targets:

```yaml
catalog:
  profileGroups:
    member: Members
    staff: Team
    admin: Team
  groupOrder: [Team, Members]
  featureTags:
    feedback.enabled: feedback
    feedback.rate: feedback
```

- `profileGroups` maps exact profile names to display labels; multiple profiles can share
  a label. Unmapped profiles keep their own names, and an empty label uses that fallback.
- `groupOrder` optionally lists group labels to show first. Remaining groups follow
  alphabetically, so new profiles stay visible. Labels with no items are omitted.
- `featureTags` optionally maps exact Remote Config keys to tags. A scenario or flow
  setting that key gains the mapped tag, alongside its explicit header tags. Empty
  mappings add no tag. Keys and tags can use any project vocabulary; no key prefix or
  customer naming convention is interpreted.

Metadata belongs in the shared file; there is no local catalog override. It controls
presentation only and does not change fixture selection or execution.

## Builds

Build preparation is independent of device discovery and installation. Each build runs in
that platform's source checkout (`platforms.<name>.project`, or the suite repository when
omitted). Artifact globs resolve from the same directory. Builds inherit the process
environment; YAML `env` values are Maestro inputs only.

`build.system` accepts `gradle`, `xcode`, `kotlin-toolchain` or `command`. When omitted,
Android uses Gradle and iOS uses Xcode, unless `build.command` is present. The former
`system: native` value has been replaced by these explicit platform defaults.

- **Gradle:** `task` defaults to `assembleDebug`, and `artifact` defaults to
  `app/build/outputs/apk/debug/*.apk`. Mockstro runs `./gradlew <task> -q`.
- **Xcode:** set `project` (`.xcodeproj` or `.xcworkspace`) and `scheme`;
  `configuration` defaults to `Debug`. Mockstro builds for a generic iOS Simulator with
  signing disabled and derived data in `build/maestro-derived-data`. `artifact` defaults
  to `build/maestro-derived-data/Build/Products/<configuration>-iphonesimulator/*.app`.
  Project and scheme are required when building, so configurations using an installed
  app can omit them.
- **Kotlin Toolchain:** set `system: kotlin-toolchain`; `module` defaults to the platform
  name, and `variant` defaults to `debug` (`release` is also accepted). See the
  [Kotlin Toolchain guide](kotlin-toolchain.md) for commands and artifact defaults.
- **Command:** set `command` to a nonempty argv list and supply `artifact`. This is the
  fallback for Make, mise, project scripts and other task runners. `system: command`
  is optional when `command` is present; pairing a command with another system is an error.

```yaml
platforms:
  android:
    project: ../android-app
    appId: com.example.android
    integration: full
    build:
      command: [make, app]
      artifact: out/android/*.apk
  ios:
    project: ../ios-app
    appId: com.example.ios
    integration: full
    build:
      command: [mise, run, build-ios]
      artifact: out/simulator/*.app
```

Each command element is passed literally as one argument. Spaces, globs, `$VARIABLE`,
`$(...)`, pipes and redirections receive no shell interpretation. Use a project script
for multi-step builds, or explicitly invoke a shell in the argv list when required.
No alternate command is attempted after a build fails, and existing outputs are never
accepted after a failed command. Successful incremental builds may reuse their outputs.

Exactly one installable artifact must match: a regular `.apk` file on Android, or a
`.app` directory on iOS. Globs use Go filepath syntax (`*`, `?`, and character classes;
`**` does not recurse). iOS bundles must identify the configured app, target the iOS
Simulator and contain their declared executable. Each result is copied into a new
run-owned `artifacts/build-*` directory and receives a SHA-256 hash; iOS hashes include
resources, file modes and symlinks. Bundle symlinks must be relative and resolve within
the bundle. Rebuilding cannot overwrite a previous snapshot.
The snapshot directory contains `artifact.sha256` for inspection.

## mockstro.local.yaml

Overrides for this machine only:

```yaml
platforms:
  android:
    project: /Users/me/projects/android-app
    devices: [My_Pixel]
  ios:                           # a platform missing from mockstro.yaml can be added in full
    project: ../ios-app
    appId: com.example.ios
    devices: [My_iPhone]
    build: { project: App.xcodeproj, scheme: App }
```

- For platforms already in `mockstro.yaml`, only `project`, `devices`, `build` and `env` can be overridden.
- When the UI opens a suite without `mockstro.yaml`, platforms here are defined in full (see [Browser UI](web-ui.md#starting-it)).
- `devices` replaces the shared list; `build` fields and `env` values merge.
- A local `build.command` replaces the entire argv list and selects command builds. Setting
  a local `build.system` to a native system clears an inherited command. Shared artifact
  globs remain in effect unless overridden.
- An added platform only runs on machines that define it.

## Variables

Lowest to highest precedence:

1. top-level `env`
2. platform `env`
3. flow header `env`
4. local `env` (and local platform `env`)
5. `--env-file` files, in order
6. `-e KEY=VALUE`

Variable files are flat YAML maps of scalars. `APP_ID` and `WIREMOCK_PORT` are set by
mockstro. Variables go to Maestro only, not to builds. Don't store secrets in YAML.

## Platforms

Each flow runs on every configured platform. `--platform android|ios` narrows it. Use
`appId: ${APP_ID}` in every flow and Maestro's own `when: { platform: iOS }` for
platform-specific steps; tags don't select platforms.

## Environment variables

| Variable | Default |
| --- | --- |
| `MOCKSTRO_KEEP_RUNS` | 20 finished runs kept per project |
| `MOCKSTRO_STATE_DIR` | `~/.local/state/mockstro` |
| `MOCKSTRO_CACHE_DIR` | `~/.cache/mockstro` |
