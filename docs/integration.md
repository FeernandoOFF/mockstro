# Integration: lite and full

mockstro integrates with a project in one of two ways, configured per platform in `.maestro/mockstro.yaml`:

| | Lite · proxy (drop-in) | Full · runtime override |
| --- | --- | --- |
| Configured with | `integration: lite` (default) | `integration: full` |
| App changes | none | an E2E seam in a debug/test build |
| How traffic reaches WireMock | your Proxyman/Charles Map Remote → `127.0.0.1:18080` | mockstro tells the app where WireMock is (per run, per device) |
| Build | none: runs use the app already installed | mockstro builds, installs and records provenance |
| Devices | one at a time: Android emulator/phone or iOS simulator | designated emulators/simulators; Android phones via map remote |
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
   `appId: ${APP_ID}` and `properties.apiProfile:`. Add a platform with its literal `appId`
   and `integration: lite` in `mockstro.yaml` (see [configuration](configuration.md)).
2. Install the app on the device yourself (any build that trusts your proxy's certificate).
3. In Proxyman/Charles add Map Remote rules to `http://127.0.0.1:18080`:
   - your API base URL (`https://api.example.com/*`);
   - `https://firebaseremoteconfig.googleapis.com/*` if flows use Remote Config overrides.
   Leave path/query empty and disable "Preserve Host Header".
4. `./mockstrow run` or the TUI. Each run serves its mock on port 18080, so only one runs at a time.

**Android** (emulator or phone)

- Route the device through the proxy (Proxyman's Android setup, or Wi-Fi proxy on a phone).
  mockstro leaves a device-wide proxy alone in lite.
- Install the proxy's CA on the device. The app must trust user CAs; most debug builds do
  through `debug-overrides` in `network_security_config`. If yours does not, the CA has to be
  a system CA (e.g. a writable emulator image).

**iOS** (simulator)

- Enable the Mac's system proxy for Proxyman/Charles; sharing the Mac's network alone
  does not enable interception.
- Install the proxy's certificate into the simulator (Proxyman: Certificate → Install for iOS
  Simulator), trust it, and enable HTTPS interception for the mapped hosts.
- Follow [the iOS guide](ios-integration.md#lite-install-and-route-the-app) for the
  complete setup and common routing failures. Physical iPhones are not supported by
  the current iOS runner.

## Full · runtime override

The app reads a per-run override that points it at the run's own WireMock
(`http://127.0.0.1:<port>/api`). No proxy is involved, so each device can have its own port,
which makes parallel runs possible.

**Both platforms**

1. Configure each target with `appId`, `integration: full`, source `project`, `devices` and
   native `build` settings in `.maestro/mockstro.yaml`. Optional `proxyBaseUrl`, `fallbackUrl`,
   `overrideKey` and `acceptKey` configure Android's mock seam; iOS uses fixed file names.
2. In a debug/E2E build only, add the seam:
   - on launch, read and validate the override before constructing services; Android's
     payload also identifies the app, while iOS writes to the selected installed bundle;
   - use its `apiUrl` as the API base URL;
   - apply its `remoteConfig` key/values on top of Remote Config;
   - publish the `runId` it honoured as soon as it has read the override (mockstro fails the flow if the app never does);
   - allow cleartext HTTP to `127.0.0.1`.
3. Designate reusable emulators/simulators with `platforms.android.devices` and
   `platforms.ios.devices`. For machine-specific paths or device names use the ignored
   `.maestro/mockstro.local.yaml`. The global `~/.config/mockstro/designated.txt` remains available.

With both full platforms selected, mockstro runs them concurrently on one Mac. Each worker
has an isolated mock server/port and app override. If either selected platform is lite,
platforms run sequentially through the fixed proxy port. Each flow uses `${APP_ID}` and can
branch through Maestro's native `runFlow.when.platform` condition; there is no case registry.

**Android**

Recommended structure: [android-integration.md](android-integration.md).

- Build: `platforms.android.build.task` and `platforms.android.build.artifact` select
  the Gradle task and APK glob, relative to the Android source checkout.
- Override: base64 JSON in `Settings.Global[overrideKey]`, read
  before the app builds its HTTP clients (e.g. a debug-only `ContentProvider`):
  `{"schema":"1","applicationId","runId","expiresAt" (ms),"apiUrl","remoteConfig":{}}`.
- Acceptance: write `runId` to `Settings.Global[acceptKey]` right after reading the
  override. Declare `WRITE_SECURE_SETTINGS` in that variant's manifest; mockstro grants it after
  install.
- mockstro runs `adb reverse tcp:<port>`, so `127.0.0.1` on the device reaches the Mac.
- `network_security_config`: permit cleartext to `127.0.0.1`.

For manual mocking on a connected Android phone, open the TUI's **Mock** tab,
press `a` to select an unlocked phone, then press Enter on a scenario, profile or
flow. Phone attachments reuse the installed app, clear any device-wide HTTP
proxy, and configure the runtime override and `adb reverse` on port 18080.
The installed build must support the override seam above. Press `x` to stop
and clean up the attachment; restart the app after starting or switching mocks
so it reads the new override. Only human-owned sessions can attach phones directly.
The CLI equivalent is
`./mockstrow mock <scenario> --platform android --attach --device <phone-serial> --skip-build --clear-proxy`.

**iOS** (simulators only)

Implementation and verification: [ios-integration.md](ios-integration.md), including a
Swift parser, build configuration isolation, Remote Config and acknowledgement.

- Build: `platforms.ios.build.project` (`.xcodeproj`/`.xcworkspace`), `scheme`, and `configuration`,
  relative to the iOS source checkout.
- Override: read `e2e-config.json` from the app bundle (`e2eApiUrl`, `e2eRunId`,
  `e2eRemoteConfig` (JSON string), `e2eExpiresAt` (ms)); the same values are also set as
  `E2E_API_URL`, `E2E_RUN_ID`, `E2E_REMOTE_CONFIG`, `E2E_EXPIRES_AT` in the simulator
  environment.
- Acceptance: write `runId` to `Documents/e2e-accepted-run-id` in the data container.
- ATS: allow local networking (`NSAllowsLocalNetworking`).
- The simulator shares the Mac's loopback, so nothing is forwarded.

Android phones can run flows through `--map-remote` (lite behaviour, one device),
or attach to a human-owned manual mock using the runtime override as described above.
The iOS runner currently supports simulators only.

## Additional build and transport support

For Kotlin Toolchain projects, see [kotlin-toolchain.md](kotlin-toolchain.md).
For message fixtures and the optional WebSocket endpoint in the runtime override, see
[websockets.md](websockets.md). These extend full integration's existing device and
acceptance mechanisms.
