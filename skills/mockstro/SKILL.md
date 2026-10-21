---
name: mockstro
description: End-to-end (E2E) mobile UI testing with mockstro. Use to run or debug Maestro flows on Android emulators or iOS simulators, write or edit flows, or change their WireMock API mocks (stubs, profiles, scenarios). Any task touching `.maestro/` or `./mockstrow`.
---

Run everything through the project's `./mockstrow` from the repository root; it fetches the
pinned CLI itself. `./mockstrow <command> --help` is the flag reference. Without a wrapper,
ask the maintainer to run the mockstro installer.

## Running and debugging flows

1. **Orient.** Read `.maestro/mockstro.yaml`, the optional `mockstro.local.yaml`, and the
   target flows. Done when you know which flows, which platforms, and whether each platform
   is `lite` (one device, sequential) or `full` (parallel).
2. **Claim a device.** `./mockstrow --owner agent status --json`; `--quiet` exits 0 idle,
   10 busy with a free device, 11 nothing free. You may use only designated emulators and
   simulators (the platform's `devices` list); `./mockstrow boot <name>` starts one. Ask the
   user before using a personal physical device. Done when each platform has a free
   designated device.
3. **Run.** `./mockstrow --owner agent run <flow.yaml>…` or `--tags <tags>`; `--platform`
   narrows to one platform. Test inputs go in `--env-file <yaml>` or `-e KEY=VALUE`. Add
   `--skip-build` only when the installed app already contains the change under test.
   Done when the run prints its run directory.
4. **Triage.** In the run directory read `run.json`, `result.tsv` and each failing flow's
   log. Classify every failure by its stage:
   - `TEST`: a UI assertion or app behaviour.
   - `VERIFY`: the flow passed but an `apiVerifications` count was wrong; check the stubs.
   - `SETUP`: fixtures, build, install or device; fix the environment, not the flow.
   - `OVERRIDE` (full only): the app never confirmed it read the run's mock; check the app's E2E seam.
   Flows share one Maestro process per batch; rerun with `--isolate` when a failure might
   come from an earlier flow. Done when every failing flow/platform pair has a stage,
   evidence from its log, and a rerun command.
5. **Verify.** After a fix, run the narrowest rerun: the single flow on the single platform,
   or `--rerun-failed`. Done when it passes.

Report each failure as flow, platform, stage, evidence and rerun command.

## Writing flows and mocks

Before creating or editing flows, stubs, profiles or scenarios, read
[references/fixtures.md](references/fixtures.md). Validate with `./mockstrow check`, then
verify with the narrowest rerun.

## Environment

- `./mockstrow check` validates fixtures and checks Java 17+, Maestro and WireMock. Pass
  `--install` to install a missing Maestro only when the user asked for dependency setup.
  Relay its install command when Java is missing; Android tools, Xcode and devices are the
  user's to install.
- To test a development CLI, put `--dev` first: `./mockstrow --dev run …` (uses
  `mockstro-dev` on PATH or `MOCKSTRO_DEV_CLI`).
- Leave `.maestro/.runtime/` out of commits.
