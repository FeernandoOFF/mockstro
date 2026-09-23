# trun

A Go port of `.maestro/test.sh`: runs a repository's Maestro flows against a local WireMock, on
Android emulators or iOS simulators, with a CLI for scripts/agents and a TUI for humans.

It reads the existing `.maestro/` tree unchanged (flows, scenarios, profiles, stubs with
`extends`, `project.env`), so it works in any repo that already has `test.sh`, and the two can
run side by side.

## Install

```bash
make install          # builds ~/.local/bin/trun
```

Requires Go 1.25+ to build; at runtime: Java 17+, Maestro, and `adb` (Android) or Xcode (iOS).
`jq`/`yq` are no longer needed.

## Use

```bash
trun                  # TUI (in a terminal); runs the smoke suite otherwise
trun run              # smoke suite
trun run --tags smoke,regression --parallel auto
trun run .maestro/flows/account/account-classic.yaml --skip-build
trun mock usage-flat-hourly            # server-only on :18080
trun mock smart-plus --attach --device emulator-5554
trun status [--json|--quiet]           # every active run, all projects
trun sweep [--dry-run]
trun list [profiles|scenarios] · trun resolve <stub> · trun last-run
trun boot [avd|simulator]              # list or boot a designated device
trun prune [--keep N]                  # delete old run dirs, strip APK snapshots
trun record-api · trun playback <dir> · trun setup
```

`-C <dir>` targets another repository; `--platform ios` overrides `project.env`.
Every run writes `result.tsv` (test.sh format) and `junit.xml` into its run dir;
`--junit <path>` writes an extra copy.

### TUI

| Tab       | What it does                                                                 |
|-----------|------------------------------------------------------------------------------|
| Dashboard | devices, designated/bootable AVDs (`b` boots), active runs of every project, sweep |
| Flows     | filter by text/tag, multi-select, then a run form (device, parallel, build/skip, boot, profile, fail-fast, record, map-remote, FBRC) that shows the equivalent CLI command |
| Run       | live workers, per-flow results, each worker's Maestro output; ↑↓ on a result jumps the output to where that flow started (`G` follows the tail again, `f` switches arrows to scrolling); cancel; `F` re-run failed |
| History   | past runs (trun and test.sh) with sizes, per-flow logs, `F` re-run failed, open run dir; `d` delete a run, `P` prune to the newest N, `X` delete all finished |
| Mock      | serve a scenario/profile/flow on :18080 (optionally attached to a device), live request journal with unmatched requests highlighted, switch targets without restarting |
| Stubs     | a stub's own JSON (default) or the merged result (`m`); `e`/`E` walk the `extends` chain; a second panel shows the served response with `bodyFileName` expanded; who uses it |

## Machine-wide state

| Path                              | Contents                                               |
|-----------------------------------|--------------------------------------------------------|
| `~/.local/state/trun/locks`       | device/port locks shared by every project              |
| `~/.local/state/trun/runs`        | active-run registry (what `status` shows)              |
| `~/.local/state/trun/projects.json` | repos trun has operated on                           |
| `~/.cache/trun/wiremock`          | the pinned, SHA-verified WireMock jar                  |
| `~/.config/trun/designated.txt`   | optional: AVD/simulator names any project may drive    |

Locks are also mirrored into each known repo's `<git-common-dir>/maestro/locks` in test.sh's
exact format, so a `test.sh` run in any repo trun has seen and a `trun` run never share a device
or port. Run `trun status -C <repo>` once in a repo to make it known.

Run payload (logs, sanitized flows, results) stays in the repo under
`.maestro/.runtime/runs/<runId>/`, as before. The APK snapshot (~70MB) is deleted once the run
ends (its SHA-256 stays in `apk.sha256`/`run.json`), and every run prunes the project to the
newest `TRUN_KEEP_RUNS` (default 20) finished run dirs.

## Differences from test.sh

- Parallel workers are goroutines in one process, not child `test.sh` processes; the queue,
  `INTERRUPTED`/`UNSTARTED` accounting and single shared build are unchanged.
- A **human** owner may use any emulator (designated ones first); agents/CI still need a
  designated one (`agent-avds.txt`, `MAESTRO_AGENT_AVDS`, `IOS_SIMULATOR_NAMES` or the global
  `designated.txt`). Physical devices still require `--map-remote`.
- `status --quiet` exits 0 idle, 10 active with an eligible device free, 11 active and nothing free.
- Per-flow Maestro logs are kept in `<run dir>/logs/`.
- Project-specific constants that test.sh hardcoded can be set in `project.env`:
  `E2E_PROXY_BASE_URL`, `E2E_FBRC_URL`, `E2E_OVERRIDE_SETTING_KEY`, `E2E_ACCEPT_SETTING_KEY`,
  `E2E_FALLBACK_PROXY_URL`. For iOS: `IOS_PROJECT` (`.xcodeproj` or `.xcworkspace`),
  `IOS_SCHEME`, `IOS_CONFIGURATION`, `IOS_SIMULATOR_NAMES`.

## Develop

```bash
make test             # ports of check-runner.sh's hermetic checks + TUI render tests
```
