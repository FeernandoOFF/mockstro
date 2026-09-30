# Mockstro

Runs a repository's [Maestro](https://maestro.mobile.dev) UI flows against a local
[WireMock](https://wiremock.org), on Android emulators or iOS simulators. Every flow says which
API fixtures it needs, and mockstro serves them, points the app at them, runs the flow and
reports the result. It has a CLI for scripts, agents and CI, and a TUI for humans.

- **Deterministic E2E**: each flow gets its own mocked backend, so nothing depends on a live API.
- **Parallel devices**: each device gets its own WireMock port. `--parallel auto` shares the suite across every free device.
- **Machine-wide coordination**: device and port locks are shared across every project and worktree, so concurrent runs by people, agents and CI never collide.
- **Fixture tooling**: stub inheritance (`extends`), profiles, scenarios, API call verifications, reports of unmatched requests, and recording or playback of real traffic.

## Install

```bash
make install          # builds ~/.local/bin/mockstro
```

Build: Go 1.25+. Runtime: Java 17+, the Maestro CLI, and `adb` (Android) or Xcode (iOS).
`mockstro setup` validates the project's fixtures and fetches the pinned WireMock jar, whose
SHA is verified.

## Project layout

mockstro works on the nearest parent directory that contains `.maestro/`. Use `-C <dir>` to target another one.

```
.maestro/
├── config.yaml
├── project.env               # optional: enables full integration (see below)
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
appId: com.example.app
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
| Enabled by | `.maestro/` only | `.maestro/project.env` |
| App changes | none | a debug-only E2E seam |
| Traffic to the mock | your Proxyman/Charles Map Remote → `127.0.0.1:18080` | the app reads a per-run override |
| Build | uses the installed app | mockstro builds and installs it |
| Devices | one at a time | many in parallel |

Start with lite to try it on an existing app. Move to full for parallel runs, CI, and proof that
the app actually used the mock. Setup for each: [docs/integration.md](docs/integration.md).
Recommended Android seam: [docs/android-integration.md](docs/android-integration.md).

## Usage

```bash
mockstro                                   # TUI in a terminal; runs the smoke suite otherwise
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

## Integrating with CI and agents

- **Reports**: every run writes `result.tsv`, `junit.xml`, `run.json` and per-flow Maestro logs
  to `.maestro/.runtime/runs/<runId>/`. `--junit <path>` writes an extra copy of the report
  where CI expects it.
- **Progress**: `progress.tsv` in the run dir is rewritten as each flow finishes (done flows as
  `result|…`, the rest as `plan|…`); the TUI's History tab uses it to follow headless runs live.
- **Exit status**: `run` exits 0 when every flow passed, 1 otherwise, and 130 if interrupted.
- **Scheduling**: `mockstro status --quiet` exits 0 when idle, 10 when runs are active and a
  device is free, and 11 when runs are active and nothing is free. `status --json` gives the
  full picture.
- **Owners**: `--owner human|agent|ci` (auto-detected by default). People may use any emulator.
  Agents and CI use only designated devices: `MAESTRO_AGENT_AVDS` / `IOS_SIMULATOR_NAMES` in
  `project.env`, or `~/.config/mockstro/designated.txt` for machine-specific names.
  `--boot-emulator` boots one when none is free, and `--headless` runs it without a window.
- **Physical devices** always run through `--map-remote` (lite behaviour, one device).

## Configuration

`.maestro/project.env` (full integration):

| Key | Purpose |
| --- | --- |
| `MAESTRO_PLATFORM` | `android` or `ios` |
| `APP_PACKAGE` | application id / bundle id |
| `MAESTRO_AGENT_AVDS`, `IOS_SIMULATOR_NAMES` | designated devices |
| `E2E_PROXY_BASE_URL`, `E2E_FALLBACK_PROXY_URL`, `E2E_FBRC_URL` | real API and Remote Config URLs |
| `E2E_OVERRIDE_SETTING_KEY`, `E2E_ACCEPT_SETTING_KEY` | Android override and acceptance keys |
| `MAESTRO_APP_VARIANT`, `ANDROID_GRADLE_TASK`, `ANDROID_APK_GLOB` | Android build |
| `IOS_PROJECT`, `IOS_SCHEME`, `IOS_CONFIGURATION` | iOS build |

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
is deleted (its SHA-256 is kept in `run.json`), and the project is pruned to the newest
`MOCKSTRO_KEEP_RUNS` finished runs.

## Develop

```bash
make test             # hermetic engine checks + TUI render tests
make vet
```
