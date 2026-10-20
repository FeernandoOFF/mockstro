<p align="center">
  <img src="docs/assets/monstruo.png" alt="Mockstro's friendly mint monster mascot holding a conductor's baton" width="220">
</p>

# Mockstro

**Deterministic mobile UI testing, orchestrated.**

Mockstro brings Maestro flows and WireMock API fixtures together in a Go CLI and TUI
for reliable Android and iOS end-to-end tests—locally, with agents, and in CI.

Runs a repository's [Maestro](https://maestro.mobile.dev) UI flows against a local
[WireMock](https://wiremock.org), on Android emulators or iOS simulators. Every flow says which
API fixtures it needs, and mockstro serves them, points the app at them, runs the flow and
reports the result. It has a CLI for scripts, agents and CI, and a TUI for humans.

- **Deterministic E2E**: each flow gets its own mocked backend, so nothing depends on a live API.
- **Both platforms**: shared `${APP_ID}` flows run on each configured target. Full targets run concurrently; lite targets cause sequential execution.
- **Parallel devices**: each worker gets its own WireMock port. `--parallel auto` distributes flows across free devices within each full target.
- **Machine-wide coordination**: device and port locks are shared across every project and worktree, so concurrent runs by people, agents and CI never collide.
- **Fixture tooling**: stub inheritance (`extends`), profiles, scenarios, API call verifications, reports of unmatched requests, and recording or playback of real traffic.

## Install

From an app repository that already has `.maestro/`, run the project installer.
No global mockstro or Go installation is needed:

```bash
# Run after this release is published.
version=v0.1.0-rc.1
installer=$(mktemp)
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
  "https://github.com/FeernandoOFF/mockstro/releases/download/$version/install.sh" \
  -o "$installer"
sh "$installer" "$version" "$PWD"
rm "$installer"

./mockstrow --version
./mockstrow skill install --claude   # optional Claude link; canonical skill is in .agents/skills
# Commit mockstrow, .maestro/.mockstro-version and the installed skill/link.
```

The installer verifies the released wrapper, uses it to download and verify the pinned
CLI, then calls that CLI's bootstrap command. It preserves `.maestro/` configuration
and refuses to overwrite an existing wrapper or pin. It installs no global binary.
If a CLI is already available, `mockstro bootstrap --cli-version vX.Y.Z` adds the same files.
The release workflow must publish the new assets before installation from GitHub works;
older releases may lack them. Source installation remains `make install` (Go 1.25.6+).

After cloning the app, teammates simply run:

```bash
./mockstrow setup
./mockstrow run         # smoke flows; ./mockstrow opens the TUI in a terminal
```

Stable and prerelease pins (such as `v0.1.0-rc.1`) are supported.
The wrapper supports macOS/Linux on arm64/amd64, downloads the exact pinned release,
verifies SHA256, and atomically caches the binary under `~/.cache/mockstro/cli/`
(or `MOCKSTRO_CACHE_DIR`). It verifies cached binaries before reuse, works offline
after the first download, and never substitutes a global `mockstro`. Installation and
first use need `curl` and `sha256sum` or `shasum`.

Runtime: Java 17+, the Maestro CLI, and `adb` (Android) or Xcode (iOS), plus a configured
device. `./mockstrow setup` validates fixtures and fetches the checksum-pinned WireMock jar.
External mobile tools and devices are managed separately.

The built-in skill ships with the CLI release. `skill install` copies it into
`.agents/skills/mockstro`; `--claude` adds a relative `.claude/skills/mockstro` symlink to
the same files. Existing skills/links are preserved. For upgrades, change the version pin;
review and remove the previous skill directory/link before explicitly reinstalling from
the new CLI. Keep local edits separately if you want to retain them.

## Project layout

mockstro works on the nearest parent directory that contains `.maestro/`. Use `-C <dir>` to target another one.

```
.maestro/
├── .mockstro-version         # CLI release pin read by the root mockstrow launcher
├── config.yaml
├── mockstro.yaml             # platforms, app IDs, builds, devices and test defaults
├── mockstro.local.yaml       # ignored machine overrides (optional)
├── flows/<feature>/*.yaml    # Maestro flows, grouped by feature
├── shared/                   # sub-flows reused through runFlow
├── scenarios/*.yaml          # named mock setups for `mockstro mock`
└── wiremock/
    ├── stubs/*.json          # WireMock mappings; may `extends` another stub or a base
    ├── bases/*.json          # response templates that are only inherited
    ├── __files/              # response bodies (bodyFileName)
    └── profiles/*.json       # {"mappings": [...stub names]}; `shared.json` is always loaded
```

A flow's header declares its app, tags and mock:

```yaml
appId: ${APP_ID}
tags: [smoke, account]
properties:
  apiProfile: signed-in          # profiles/signed-in.json, on top of shared.json
  apiOverrides: [account-empty]  # extra stubs; replace same-route stubs from the profile
  fbrcOverrides:                 # Remote Config values (always strings)
    new_home_enabled: "true"
  apiVerifications:              # checked against the mock's request log after the flow
    - stub: account-empty
      atLeast: 1                 # or exactly / atMost
---
- launchApp
- assertVisible: "No accounts yet"
```

Stubs inherit `response` only. `request`, `name`, `priority` and scenario fields stay each
stub's own. In a child stub, `"__remove__"` deletes an inherited key and `null` sets it to
null.

## Integration: lite or full

| | Lite · proxy | Full · runtime override |
| --- | --- | --- |
| Configured per platform | `integration: lite` (default) | `integration: full` |
| App changes | none | a debug-only E2E seam |
| Traffic to the mock | your Proxyman/Charles Map Remote → `127.0.0.1:18080` | the app reads a per-run override |
| Build | uses the installed app | mockstro builds and installs it |
| Devices | one at a time | many in parallel |

Start with lite to try it on an existing app. Move to full for parallel runs, CI, and proof that
the app actually used the mock. Setup for each: [docs/integration.md](docs/integration.md).
Recommended Android seam: [docs/android-integration.md](docs/android-integration.md).
iOS setup and Swift seam: [docs/ios-integration.md](docs/ios-integration.md).

## Usage

```bash
mockstro                                   # TUI in a terminal; runs the smoke suite otherwise
mockstro init --android-app-id com.example.app --ios-app-id com.example.ios
mockstro run                               # flows tagged smoke
mockstro run --tags smoke,regression --parallel auto
mockstro run .maestro/flows/account/overview.yaml --skip-build
mockstro run --platform ios --device <udid>
mockstro list [profiles|scenarios]         # flows by default
mockstro resolve <stub>                    # a stub with its extends chain applied
mockstro mock <profile|scenario>           # serve it on :18080 for manual testing
mockstro mock <scenario> --attach --device emulator-5554
mockstro boot [avd|simulator]              # list or boot a designated device
mockstro status [--json|--quiet]           # active runs, devices and ports, all projects
mockstro last-run
mockstro sweep [--dry-run]                 # reclaim locks left by crashed runs
mockstro prune [--keep N]
mockstro record-api · mockstro playback <dir>
```

`mockstro <command> --help` lists every flag.

`record-api` starts a recording proxy; route the app through its printed local address
and press Ctrl-C to save mappings below `.maestro/.runtime/wiremock/recordings/`.
Set `--target https://your-api.example.com` (or the platform's `proxyBaseUrl`). The
default capture filter is `/api/.*`; use `--url-path-pattern '/.*'` when the app also
uses paths such as `/graphql`, `/v1/`, or discovery endpoints outside `/api/`.
The recorder connects directly to the upstream, bypassing JVM/system HTTP proxy
settings so a Map Remote rule cannot send its own requests back into the recorder.

Flows run in batches: one Maestro process per device runs several flows, so its driver startup
(~15s on iOS, ~5s on Android) is paid once per batch. Each flow still gets its own mock
scenario, override, checks and log; mockstro injects `onFlowStart`/`onFlowComplete` hooks into
its working copy of each flow. Use `--isolate` to run one Maestro process per flow when
debugging; `--record` always does.

### Result stages

Each flow reports a stage and an exit code. A non-zero exit means it failed at that stage.

| Stage | Meaning |
| --- | --- |
| `PASS` | the flow passed |
| `TEST` / `RECORD` | Maestro failed while running or recording the flow |
| `VERIFY` | the flow passed, but an `apiVerifications` count did not match |
| `SETUP` | setup failed before the flow ran |
| `OVERRIDE` | the app never confirmed it read this run's override (full only) |
| `CANCELLED` | stopped by the user |
| `INTERRUPTED` | the run ended while the flow was in progress |
| `UNSTARTED` | the run ended before the flow started |

### TUI

Tabs (`1`–`6`, `tab`, `h`/`l`):

| Tab | What it does |
| --- | --- |
| Dashboard | devices and bootable AVDs/simulators (boot, boot headless, stop, clear proxy, toggle keyboard), active runs of every project, sweep |
| Flows | filter by tag or text, multi-select, open a run form that shows the equivalent CLI command |
| Run | live workers, per-flow results, each worker's Maestro output; cancel, re-run failed |
| History | past runs with per-flow logs and sizes; re-run failed, delete, prune |
| Mock | browse scenarios, profiles and flows; serve one on :18080, optionally attached to a device; live request journal with unmatched requests highlighted |
| Stubs | a stub's own JSON or merged result, its `extends` chain, the served response, and which flows use it |

Shared keys: `↑↓`/`jk` move, `u`/`d` page, `g`/`G` top/bottom, `enter`/`f` focus right, `/`
filter, `space` select, `x` stop/delete, `y` copy, `r` refresh, `q` quit.

Press `e` on a flow (including Run/History results), mock scenario/profile/flow, or
stub to open its source in `$EDITOR` (`$VISUAL`, then `vi` if unset). Editor arguments
and quoted paths are supported, for example `EDITOR="code --wait"`. The TUI resumes
and reloads fixtures when the editor exits. In Stubs, `[`/`]` walk the inheritance
chain; `e` in the Own JSON pane edits the ancestor currently shown. In Mock, `e` in
the right pane edits the highlighted stub. Press enter on a mock target to apply
saved fixture changes to a running mock server.

## Integrating with CI and agents

- **Reports**: every run writes `result.tsv`, `junit.xml`, `run.json` and per-flow Maestro logs
  to `.maestro/.runtime/runs/<sessionId>/`, with native run metadata below each platform. `--junit <path>` writes an extra copy of the report
  where CI expects it.
- **Progress**: `progress.tsv` in the run dir is rewritten as each flow finishes (done flows as
  `result|…`, the rest as `plan|…`); the TUI's History tab uses it to follow headless runs live.
- **Exit status**: `run` exits 0 when every flow passed, 1 otherwise, and 130 if interrupted.
- **Scheduling**: `mockstro status --quiet` exits 0 when idle, 10 when runs are active and a
  device is free, and 11 when runs are active and nothing is free. `status --json` gives the
  full picture.
- **Owners**: `--owner human|agent|ci` (auto-detected by default). People may use any emulator.
  Agents and CI use only designated devices: `platforms.android.devices` / `platforms.ios.devices` in
  `mockstro.yaml` or `mockstro.local.yaml`, or `~/.config/mockstro/designated.txt` for machine-specific names.
  `--boot-emulator` boots one when none is free, and `--headless` runs it without a window.
  Fresh Android boots wait for three CPU samples at least 60% idle (two seconds apart),
  capped at three minutes, to reduce contention from startup work. The wait is best-effort.
  For iOS, headless does not open Simulator.app; the simulator and app still render and run.
  It leaves an already-open Simulator window alone.
- **Physical devices** always run through `--map-remote` (lite behaviour, one device).

## Configuration

`.maestro/mockstro.yaml` is required. Settings are explicit; `project.env` is no longer read.
Start a new suite with:

```bash
mockstro init --android-app-id com.example.android --ios-app-id com.example.ios \
  --android-project ../android-app --ios-project ../ios-app
```

The template uses lite integration, a shared `appId: ${APP_ID}` smoke flow, and ignores
`mockstro.local.yaml` and `.runtime/`. Add assertions to the starter flow. Each platform
can independently opt into full integration once its debug app supports the override seam.

```yaml
# .maestro/mockstro.yaml
platforms:
  android:
    project: ../android-app
    appId: com.example.android
    integration: full
    devices: [Agent_Pixel]
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
env:
  TEST_USER: demo
```

Source project paths are relative to the owning suite repository, including in local
overrides. They can point at the same checkout for a monorepo or separate checkouts.
All flows, shared subflows and mocks belong to this suite's `.maestro/`.
Use `mockstro.local.yaml` to override only `env` or each platform's `project`, `devices`,
`build`, and `env`. Local files cannot redefine test cases, app IDs or integration modes.

```yaml
# .maestro/mockstro.local.yaml — ignored
platforms:
  android:
    project: /Users/me/projects/android-app
    devices: [My_Pixel]
```

Each selected flow runs on every configured platform by default. On a Mac, full targets
run concurrently with separate device/port locks and WireMock servers. If any selected
target is lite (or `--map-remote` is used), platforms run sequentially; lite always uses
one installed app/device at a time. `--parallel 2` gives each full target up to two device
workers, booting designated devices if needed. With both full targets, `--parallel 2`
uses two Android devices and two iOS simulators (four workers total). `--parallel auto`
uses running, free devices and can produce different counts per platform; when none
are running, it boots one. The TUI defaults to `Workers per platform: 1`; select `2`
for two devices per platform, or `auto` to use the available running devices. Lite targets
remain single-device. One platform failing does not stop the
other. Cancellation stops the suite and cleans up both targets.

```bash
./mockstrow run .maestro/flows/smoke/launch.yaml
./mockstrow run --platform ios --tags smoke --skip-build
./mockstrow run --env-file .maestro/test-users.yaml -e TEST_USER=other
./mockstrow run --rerun-failed
```

Variable files are flat YAML maps with non-null scalar values, converted to strings.
Precedence, lowest to highest: suite `env`, platform `env`, flow header `env`, local `env`
(and local platform `env`), `--env-file` files in order, then repeatable `-e KEY=VALUE`.
`APP_ID` and `WIREMOCK_PORT` are runner-controlled and cannot be set in those maps.
Build processes inherit the real process environment; test variables are passed only to
Maestro. YAML is a configuration format, not secret storage.

Use `appId: ${APP_ID}` in every flow, including subflows. Maestro's native conditions
remain unchanged:

```yaml
- runFlow:
    when:
      platform: iOS
    file: ../../shared/ios/permissions.yaml
```

Tags such as `ios` are ordinary selection labels; mockstro does not infer platforms from
tags, names or conditional steps. Reports identify each flow/platform pair. The TUI shows
one flow row with configured platform badges, a target selector and variable inputs; Run
and History distinguish platform results and rerun only failed pairs. History reruns use
current configuration/input files; secrets and resolved test values are not saved in reports.

Other per-platform options: `proxyBaseUrl`, `fallbackUrl`, `fbrcUrl`, `overrideKey`,
`acceptKey`. Android build defaults to `assembleDebug` and `app/build/outputs/apk/debug/*.apk`;
iOS requires `build.project` and `build.scheme` when building. App IDs are always explicit.
Kotlin toolchain builds are future work; execution targets already keep source roots separate.

Environment: `MOCKSTRO_KEEP_RUNS` (finished run dirs kept per project, default 20),
`MOCKSTRO_STATE_DIR`, `MOCKSTRO_CACHE_DIR`.

### Machine-wide state

| Path | Contents |
| --- | --- |
| `~/.local/state/mockstro/locks` | device and port locks shared by every project |
| `~/.local/state/mockstro/runs` | active-run registry (what `status` shows) |
| `~/.cache/mockstro/wiremock` | the pinned WireMock jar |
| `~/.config/mockstro/designated.txt` | optional AVD or simulator names any project may drive |

Run data stays in the repository under `.maestro/.runtime/`. After each run, the APK snapshot
is deleted (its SHA-256 is kept in the child run metadata), and the project is pruned to the newest
`MOCKSTRO_KEEP_RUNS` finished runs.

## Develop

Install the current checkout as a separate command on your PATH:

```bash
make install-dev                     # installs ~/.local/bin/mockstro-dev
cd /path/to/your/app
mockstro-dev tui
mockstro-dev --version                # dev-prefixed Git version, including dirty state
```

Run `make install-dev` again after changing the source. The released `mockstro` and
the project's pinned `./mockstrow` remain independent. `make build-dev` only builds
`bin/mockstro-dev`; `make uninstall-dev` removes the development command.
Override `BIN_DIR` if needed, for example `make install-dev BIN_DIR="$HOME/bin"`.

```bash
make test             # hermetic engine checks + TUI render tests
make vet
```

### Release assets

`scripts/build-release.sh vX.Y.Z` builds four static CLI binaries, the standalone
`mockstrow`, `install.sh`, and `checksums.txt` into an empty `dist/` directory. The canonical skill is
embedded from `skills/mockstro/`; the canonical launcher is `internal/bootstrap/mockstrow`.
The GitHub workflow tests and vets before building. Manual dispatch uploads build artifacts
only; pushing a version tag publishes a GitHub release (marked as a prerelease for tags such as `v0.1.0-rc.1`). No release is published by local builds.
Checksums detect corruption against release metadata fetched over HTTPS; they are not
independent signatures or protection against a compromised release publisher.

## WebSockets and Kotlin Toolchain

Full integration supports Kotlin Toolchain builds with per-platform `build.system: kotlin-toolchain`.
Native WebSocket fixtures are available with the checksum-pinned
`wiremockVersion: 4.0.0-beta.40`; HTTP-only projects keep WireMock 3.13.2 by default.
See [Kotlin Toolchain setup](docs/kotlin-toolchain.md) and
[WebSocket fixtures, verification and pushes](docs/websockets.md).
