# Configuration

Everything lives in `.maestro/mockstro.yaml` (committed) and the optional
`.maestro/mockstro.local.yaml` (ignored, per machine). Start a new suite with:

```bash
mockstro init --android-app-id com.example.android --ios-app-id com.example.ios \
  --android-project ../android-app --ios-project ../ios-app
```

This creates a lite config, a starter smoke flow, and ignores `mockstro.local.yaml` and `.runtime/`.

## mockstro.yaml

```yaml
cliVersion: v0.1.0-rc.2          # release used by ./mockstrow
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
| `build` | Android defaults: `assembleDebug`, `app/build/outputs/apk/debug/*.apk`. iOS needs `project` and `scheme`. Kotlin: `system: kotlin-toolchain`, see [the guide](kotlin-toolchain.md) |
| `wiremockVersion` | `3.13.2` (default) or `4.0.0-beta.40` for [WebSockets](websockets.md) |
| `proxyBaseUrl`, `fallbackUrl`, `fbrcUrl`, `overrideKey`, `acceptKey` | advanced |

Unknown keys print a warning and are ignored, so newer configs still load.

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
- `devices` replaces the shared list; `build` and `env` merge.
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
