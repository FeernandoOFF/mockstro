# Integration: lite and full

mockstro integrates with a project in one of two ways, picked automatically:

| | Lite · proxy (drop-in) | Full · runtime override |
| --- | --- | --- |
| Detected when | `.maestro/` exists, **no** `.maestro/project.env` | `.maestro/project.env` exists |
| App changes | none | an E2E seam in a debug/test build |
| How traffic reaches WireMock | your Proxyman/Charles Map Remote → `127.0.0.1:18080` | mockstro tells the app where WireMock is (per run, per device) |
| Build | none: runs use the app already installed | mockstro builds, installs and records provenance |
| Devices | one at a time: emulator, simulator or phone | designated emulators/simulators; phones still via map remote |
| Parallel runs | no | yes (`--parallel N\|auto`) |
| `mock --attach` | no (server-only mock) | yes |
| Proof the app used this run's mock | no | yes (acceptance signal) |
| API verifications, unmatched-request report | yes | yes |

The TUI shows which one applies in its header and on the Dashboard; options that need full are
greyed out with the reason.

## Lite · proxy (drop-in)

Nothing in the app or its network config changes. You bring the proxy.

**Both platforms**

1. Add `.maestro/`: `config.yaml`, `flows/`, `wiremock/stubs`, `wiremock/__files`,
   `wiremock/profiles` (with `shared.json`), optional `scenarios/`. Each flow declares
   `appId:` (the app mockstro targets) and `properties.apiProfile:`. Do not add `project.env`.
2. Install the app on the device yourself (any build that trusts your proxy's certificate).
3. In Proxyman/Charles add Map Remote rules to `http://127.0.0.1:18080`:
   - your API base URL (`https://api.example.com/*`);
   - `https://firebaseremoteconfig.googleapis.com/*` if flows use Remote Config overrides.
   Leave path/query empty and disable "Preserve Host Header".
4. `mockstro run` or the TUI. Each run serves its mock on port 18080, so only one runs at a time.

**Android** (emulator or phone)

- Route the device through the proxy (Proxyman's Android setup, or Wi-Fi proxy on a phone).
  mockstro leaves a device-wide proxy alone in lite.
- Install the proxy's CA on the device. The app must trust user CAs; most debug builds do
  through `debug-overrides` in `network_security_config`. If yours does not, the CA has to be
  a system CA (e.g. a writable emulator image).

**iOS** (simulator)

- The simulator uses the Mac's network, so Proxyman on the Mac sees its traffic.
- Install the proxy's certificate into the simulator (Proxyman: Certificate → Install for iOS
  Simulator).

## Full · runtime override

The app reads a per-run override that points it at the run's own WireMock
(`http://127.0.0.1:<port>/api`). No proxy is involved, so each device can have its own port,
which makes parallel runs possible.

**Both platforms**

1. Add `.maestro/project.env`. Typical keys: `MAESTRO_PLATFORM`, `APP_PACKAGE`,
   `E2E_PROXY_BASE_URL` (the real API, used for fallbacks and manual instructions),
   `E2E_FALLBACK_PROXY_URL`, `E2E_OVERRIDE_SETTING_KEY`, `E2E_ACCEPT_SETTING_KEY`.
2. In a debug/E2E build only, add the seam:
   - on launch, read the override; ignore it if it has expired or names another app;
   - use its `apiUrl` as the API base URL;
   - apply its `remoteConfig` key/values on top of Remote Config;
   - publish the `runId` it honoured as soon as it has read the override (mockstro fails the flow if the app never does);
   - allow cleartext HTTP to `127.0.0.1`.
3. Designate reusable emulators/simulators for agents/CI in `project.env`:
   `MAESTRO_AGENT_AVDS=Pixel_9a,Maestro_Pixel_6_API_30_1` (Android) or `IOS_SIMULATOR_NAMES`
   (iOS). For machine-specific names use `~/.config/mockstro/designated.txt` instead.

**Android**

Recommended structure: [android-integration.md](android-integration.md).

- Build: `MAESTRO_APP_VARIANT` (`debugEnvTest`, the default, or `envtest`);
  `ANDROID_GRADLE_TASK` / `ANDROID_APK_GLOB` override the `debugEnvTest` task and APK path.
- Override: base64 JSON in `Settings.Global[E2E_OVERRIDE_SETTING_KEY]`, read
  before the app builds its HTTP clients (e.g. a debug-only `ContentProvider`):
  `{"schema":"1","applicationId","runId","expiresAt" (ms),"apiUrl","remoteConfig":{}}`.
- Acceptance: write `runId` to `Settings.Global[E2E_ACCEPT_SETTING_KEY]` right after reading the
  override. Declare `WRITE_SECURE_SETTINGS` in that variant's manifest; mockstro grants it after
  install.
- mockstro runs `adb reverse tcp:<port>`, so `127.0.0.1` on the device reaches the Mac.
- `network_security_config`: permit cleartext to `127.0.0.1`.

**iOS** (simulators only)

- Build: `IOS_PROJECT` (`.xcodeproj`/`.xcworkspace`), `IOS_SCHEME`, `IOS_CONFIGURATION`.
- Override: read `e2e-config.json` from the app bundle (`e2eApiUrl`, `e2eRunId`,
  `e2eRemoteConfig` (JSON string), `e2eExpiresAt` (ms)); the same values are also set as
  `E2E_API_URL`, `E2E_RUN_ID`, `E2E_REMOTE_CONFIG`, `E2E_EXPIRES_AT` in the simulator
  environment.
- Acceptance: write `runId` to `Documents/e2e-accepted-run-id` in the data container.
- ATS: allow local networking (`NSAllowsLocalNetworking`).
- The simulator shares the Mac's loopback, so nothing is forwarded.

A phone under full integration still runs with `--map-remote` (lite behaviour, one device).
