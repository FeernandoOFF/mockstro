# iOS integration

Start with **lite** to run an installed app through Proxyman or Charles. Use
**full** when you want mockstro to build the app and give each simulator its own
WireMock server, without a proxy. Both modes run the same Maestro flows.

The current iOS runner supports **simulators on a Mac**, not physical iPhones.
Commands below use your project wrapper, `./mockstrow`.

## Configure the suite

The repository containing `.maestro/` owns the flows and fixtures. `project` is
relative to that repository, even if the iOS app lives in a sibling checkout:

```yaml
# .maestro/mockstro.yaml
platforms:
  ios:
    project: ../ios-app # use . if the suite lives in the iOS repository
    appId: com.example.app.test
    integration: lite
    proxyBaseUrl: https://api.example.test
    build:
      project: App.xcodeproj # or App.xcworkspace, relative to the iOS checkout
      scheme: App-Test
      configuration: TestEnv
```

Use the app's literal bundle identifier, not its display name or an extension's
identifier. The configuration must match the scheme's intended test build.
`build` settings are used in full mode; lite always uses the installed app.

Keep machine-specific paths and simulator names in the ignored local file:

```yaml
# .maestro/mockstro.local.yaml
platforms:
  ios:
    devices: [iPhone 17e] # an available simulator name on this Mac
    # project: /absolute/path/to/your/ios-checkout
```

List names/UDIDs with `xcrun simctl list devices available`. Add
`mockstro.local.yaml` and `.runtime/` to `.maestro/.gitignore`.

## Lite: install and route the app

1. Build and install your test app on the chosen simulator. mockstro does not
   compile or install in lite mode, including when `--skip-build` is omitted.
2. Start Proxyman/Charles and enable its macOS system proxy. Sharing the Mac's
   network does not by itself enable interception.
3. Install and trust the proxy's CA in that simulator. Enable HTTPS interception
   for the API and, if used, Firebase Remote Config. Certificate pinning needs a
   test-build configuration that permits this proxy.
4. Add Map Remote rules, using your API host in the first row:

| Match | Map to |
| --- | --- |
| `https://api.example.test/*` | `http://127.0.0.1:18080` |
| `https://firebaseremoteconfig.googleapis.com/*` | `http://127.0.0.1:18080` |

Leave the destination path/query empty and disable **Preserve Host Header**. The
original request path must survive: `/api/splash/iOS/...` must still reach
`/api/splash/iOS/...` on WireMock. Include a nonstandard source port if your API
uses one. The Firebase rule is needed for flow `fbrcOverrides` in lite mode.

```sh
./mockstrow check
./mockstrow run --platform ios --boot-emulator .maestro/flows/smoke/launch.yaml
```

The installed app's real API URL stays unchanged. The proxy redirects it while
WireMock is running. A connection-refused error outside a run means there is no
server listening; use `./mockstrow mock --platform ios <profile>` for manual work.
Only one lite run can own port 18080. Selecting Android and iOS together runs the
platforms sequentially if either is lite.

For example, from the iOS source checkout, build a simulator app (replace the
project, scheme, configuration and UDID with yours):

```sh
xcodebuild -project App.xcodeproj -scheme App-Test -configuration TestEnv \
  -sdk iphonesimulator -destination 'generic/platform=iOS Simulator' \
  -derivedDataPath build/maestro-derived-data CODE_SIGNING_ALLOWED=NO build
# On a booted simulator; use the actual .app name produced by your scheme.
xcrun simctl install <udid> \
  build/maestro-derived-data/Build/Products/TestEnv-iphonesimulator/App-Test.app
```

Run mockstro commands from the owning suite repository after installing.

## Full: add a simulator-only runtime override

Keep `integration: lite` until the following app changes are implemented. Then
switch it to `full`. Setting the YAML value alone cannot add the seam to an app.

### 1. Restrict the seam to the E2E configuration

In the test configuration's **Active Compilation Conditions**, add
`$(inherited) MOCKSTRO_E2E`. Do not add it to configurations used for App Store,
TestFlight or other distributed builds. A configuration called `TestEnv` does not
automatically define `DEBUG`.

Guard the parser and its call sites with:

```swift
#if MOCKSTRO_E2E && targetEnvironment(simulator)
// Test-only override parsing, application and acknowledgement.
#endif
```

Keep the app's normal environment/configuration interface available in every
build. Only its E2E implementation reads mockstro files. See Apple's
[compilation conditions](https://docs.swift.org/swift-book/documentation/the-swift-programming-language/statements/#Conditional-Compilation-Block)
and [build settings reference](https://developer.apple.com/documentation/xcode/build-settings-reference).

### 2. Read the installed bundle before building services

Before every flow, mockstro writes `e2e-config.json` into the **installed app
bundle**. Do not add this file to Xcode's Copy Bundle Resources or commit it.
The file contains strings, including a JSON-encoded string for Remote Config:

```json
{
  "e2eApiUrl": "http://127.0.0.1:18081/api",
  "e2eRunId": "example-run-id",
  "e2eRemoteConfig": "{\"feature_example\":\"true\"}",
  "e2eExpiresAt": "1790000000000"
}
```

The timestamp above illustrates epoch milliseconds; a real run supplies a future
expiry. This iOS payload has no `schema` or `applicationId` field. mockstro locates
the installed bundle by the configured `appId`; do not copy Android's payload
parser into the iOS app.

mockstro also sets `E2E_API_URL`, `E2E_RUN_ID`, `E2E_REMOTE_CONFIG` and
`E2E_EXPIRES_AT` through simulator `launchctl`. These are a secondary channel for
launches that inherit them, not a requirement for Maestro launches. Prefer the
bundle file: it is app-specific and survives data clearing. Do not rely on scheme
environment settings, launch arguments or a cached copy in UserDefaults.

This Foundation-only example reads and validates the current file format:

```swift
#if MOCKSTRO_E2E && targetEnvironment(simulator)
import Foundation

struct MockstroOverride: Decodable {
    let e2eApiUrl: String
    let e2eRunId: String
    let e2eRemoteConfig: String
    let e2eExpiresAt: String

    struct InvalidOverride: Error {}

    static func load(bundle: Bundle = .main) throws -> MockstroOverride? {
        let file = bundle.bundleURL.appendingPathComponent("e2e-config.json")
        guard FileManager.default.fileExists(atPath: file.path) else { return nil }
        let data = try Data(contentsOf: file)
        guard data.count <= 131_072 else { throw InvalidOverride() }
        let value = try JSONDecoder().decode(Self.self, from: data)
        guard
            let expiry = Int64(value.e2eExpiresAt),
            Double(expiry) > Date().timeIntervalSince1970 * 1_000,
            value.e2eRunId.range(
                of: #"\A[A-Za-z0-9._-]{1,128}\z"#, options: .regularExpression
            ) != nil,
            let url = URLComponents(string: value.e2eApiUrl),
            url.scheme == "http", url.host == "127.0.0.1",
            let port = url.port, (1...65535).contains(port),
            url.path == "/api", url.user == nil, url.password == nil,
            url.query == nil, url.fragment == nil
        else { throw InvalidOverride() }
        _ = try value.remoteConfig()
        return value
    }

    func remoteConfig() throws -> [String: String] {
        try JSONDecoder().decode(
            [String: String].self, from: Data(e2eRemoteConfig.utf8)
        )
    }

    func acknowledge() throws {
        let documents = try FileManager.default.url(
            for: .documentDirectory, in: .userDomainMask,
            appropriateFor: nil, create: true
        )
        try Data(e2eRunId.utf8).write(
            to: documents.appendingPathComponent("e2e-accepted-run-id"),
            options: .atomic
        )
    }
}
#endif
```

Call `load()` once at process startup, **before** constructing HTTP clients,
singletons or the dependency graph. When present:

1. Install `e2eApiUrl` as the effective API base URL, including its `/api` path.
   Make sure endpoint builders do not append a second `/api` or reuse an older
   client. Route derived API URLs through the same environment object.
2. Install the local Remote Config implementation described below.
3. Call `acknowledge()` only after those implementations are selected. Handle any
   error with a clear test-build diagnostic; never acknowledge a rejected payload.

An absent file permits ordinary development behavior. A malformed or expired
file should produce an explicit E2E startup failure rather than silently sending
that test's requests to the normal backend. Log the reason/run ID, not the entire
payload. Do not fetch configuration asynchronously after the first API request.

### 3. Keep Remote Config deterministic

Put Remote Config reads behind the app's existing configuration interface:

- With an active override, read bundled defaults and overlay `remoteConfig()`.
  Avoid Firebase fetches and previously activated/cached values for this process.
- Without an override, use the existing Firebase implementation.
- Match your production value conversions and defaults for missing keys. Include
  strings, booleans and numeric values; all wire values are strings.

Seeding Firebase and racing `fetchAndActivate()` is not equivalent: persisted
values can leak between flows. URLs supplied through configuration must also use
the effective mock endpoint when they address the API being mocked.

### 4. Allow loopback HTTP in the test build

The runner currently sends plain HTTP to `127.0.0.1:<port>`. Give the E2E
configuration its own Info.plist/ATS settings. Apple's local-network setting is:

```xml
<key>NSAppTransportSecurity</key>
<dict>
    <key>NSAllowsLocalNetworking</key>
    <true/>
</dict>
```

Check the **built app's** Info.plist and verify requests on each supported iOS
runtime; ATS behavior for numeric addresses varies across OS versions. Consult
[Apple's NSAllowsLocalNetworking documentation](https://developer.apple.com/documentation/bundleresources/information-property-list/nsapptransportsecurity/nsallowslocalnetworking)
when diagnosing an ATS rejection. Keep test exceptions out of shipping builds.
The simulator shares the Mac's loopback; no `adb reverse`, LAN address or proxy
certificate is needed in full mode. Disable proxy interception for this route.

## Shared flows and app startup

Every top-level flow **and subflow** should use `appId: ${APP_ID}`. Keep literal
bundle/package IDs in `mockstro.yaml`. Use native Maestro conditions for launch
arguments, permissions, selectors or navigation that differ by platform:

```yaml
appId: ${APP_ID}
tags: [smoke]
mockstro:
  apiProfile: default
---
- runFlow:
    when:
      platform: iOS
    commands:
      - launchApp:
          stopApp: true
          clearState: false
- runFlow:
    when:
      platform: Android
    commands:
      - launchApp:
          stopApp: true
          clearState: true
- extendedWaitUntil:
    visible: "Your account balance" # replace with data from your fixture
    timeout: 30000
```

Restart the app for each scenario so it rereads the bundle file and reconstructs
services. Do not cache the override between processes or expect a running app to
reload it. Pin any required stored preferences/launch arguments in a shared launch
subflow. Wait for fixture-backed content, not merely a tab bar; negative assertions
alone can pass on an unloaded screen. Android Back actions and Android resource
IDs need equivalent iOS steps/selectors, not optional assertions.

### Fast cold starts

iOS `clearState: true` reinstalls the app, which is slow. Instead, launch with
`clearState: false` and a launch argument the E2E build handles by wiping its own
data first (UserDefaults domains, Keychain, sandbox and app-group files). One
variable then picks cold or warm on both platforms:

```yaml
# Android
- runFlow:
    when:
      true: '${(LAUNCH_MODE || "cold") != "warm"}'
    commands:
      - clearState
# iOS launchApp arguments
"-mockstroResetData": '${(LAUNCH_MODE || "cold") == "warm" ? "<false/>" : "<true/>"}'
```

```swift
#if MOCKSTRO_E2E && targetEnvironment(simulator)
    // First line of App.init(), before anything reads stored state.
    if UserDefaults.standard.bool(forKey: "mockstroResetData") { resetAllData() }
#endif
```

Launch arguments live in the argument domain, so removing the persistent domain
keeps them. Don't delete files under `Library/Preferences`; `cfprefsd` caches them.
Run warm with `./mockstrow run -e LAUNCH_MODE=warm`.

## Verify full integration

After implementing the seam and changing `integration` to `full`:

```sh
./mockstrow check
# Build/install and run one known flow first.
./mockstrow run --platform ios --boot-emulator .maestro/flows/smoke/launch.yaml
# Reuse that build only while app source/build settings are unchanged.
./mockstrow run --platform ios --skip-build .maestro/flows/smoke/launch.yaml
```

The builder uses `xcodebuild` with the configured project/workspace, scheme and
configuration, simulator SDK/destination, and `CODE_SIGNING_ALLOWED=NO`. Derived
data lives in the iOS checkout's `build/maestro-derived-data`. Resolve any Xcode
package, toolchain or scheme errors before debugging the seam.

Confirm a matching acceptance ID and actual fixture-backed behavior. The runner's
preflight only checks that it wrote the endpoint; it does not execute the Swift
parser. A matching acknowledgement proves the app accepted this **run**, not that
every request or each subsequent flow used its current scenario. Use cold launches,
API verifications, assertions and the request journal for that proof.

While the run is active, inspect the acknowledgement using an explicit UDID:

```sh
data_dir=$(xcrun simctl get_app_container <udid> com.example.app.test data)
cat "$data_dir/Documents/e2e-accepted-run-id"
```

Normal cleanup removes the bundle file, simulator environment and acknowledgement,
and stops the app. Their absence after a completed run is expected.

Also verify that a malformed/expired payload is rejected, a build without the seam
cannot pass full integration, and shipping configurations compile without the
parser/flag or E2E ATS settings. With two full targets, try a paired run, then
`--parallel 2`: each platform may use up to two workers with isolated servers.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| `OVERRIDE`: app never accepted this run | E2E flag on the actual build configuration; correct installed bundle ID; startup reads the bundle before services; acknowledgement path and write errors. Rebuild before using `--skip-build`. |
| UI passes but `OVERRIDE` fails | Lite-style proxy behavior cannot substitute for full acknowledgement. Apply the seam or configure the target as lite. |
| Matching acceptance but real/stale data | Clients created before the override; cached singleton/base URL; persisted Firebase values; app not restarted between flows. |
| ATS / `NSURLErrorDomain -1022` | Built E2E Info.plist and runtime's local HTTP policy. |
| HTTP 599 / unmatched requests | Fixture route must match the iOS path/method/version; check `/api` prefixes and active profile, not just the Android URL. |
| Lite TLS / connection-refused errors | Proxy running, system proxy enabled, simulator CA trusted, HTTPS interception and both mappings enabled, WireMock running on 18080. |
| No eligible simulator | Correct local `devices` names, available Xcode runtime, `--boot-emulator`; use `--platform ios` with an explicit `--device <udid>`. |
| Cross-platform run is sequential | A selected target is lite; both need working full integration for concurrency. |

See [the mode comparison](integration.md) and [Android's seam](android-integration.md)
for the other half of a shared suite.
