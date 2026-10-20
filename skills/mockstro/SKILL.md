---
name: mockstro
description: Run and debug a project's Maestro UI flows with mockstro and WireMock, or edit their flows, profiles, scenarios and API fixtures.
---

Use the project's `./mockstrow` from its repository root. It downloads and verifies the
pinned CLI on first use and reuses the cache offline. If missing, ask the maintainer for
the project's wrapper and version pin; bootstrap requires an explicit release version.

1. Read the project's `.maestro/` configuration and relevant flow. Use `./mockstrow --help`
   and command help to discover flags. Read `mockstro.yaml` and optional ignored
   `mockstro.local.yaml`; keep each platform's lite/proxy or full/runtime integration.
2. Inspect `./mockstrow --owner agent status --json` before using a device. Status exits
   0 when idle, 10 when active with a free device, 11 when nothing is free. Agents use
   designated devices; let mockstro manage locks and ports. Boot a designated emulator
   only when needed. Confirm any proposed use of a personal physical device with the user.
3. Run an explicit flow with `./mockstrow --owner agent run <flow.yaml>`, or the requested
   tags. All configured platforms run by default; `--platform android|ios` narrows the run.
   Use `--env-file <yaml-map>` and repeatable `-e KEY=VALUE` for test inputs. Use `--skip-build` only when the installed app is suitable for the change.
4. Read the reported run directory: `run.json`, `junit.xml`, `result.tsv` and per-flow logs.
   Separate fixture/setup/override failures from UI assertions. Report the flow, stage,
   evidence and platform-specific rerun command; verify fixes with the smallest relevant
   rerun. `run --rerun-failed` selects only failed flow/platform pairs from the last run.
   Flows share one Maestro process per batch; add `--isolate` if a failure might come
   from an earlier flow in the same batch.

Write flows with `appId: ${APP_ID}`. App IDs belong to platform config. Keep platform
branches in native Maestro conditions; tags are selection labels, not platform routing.
`APP_ID` and `WIREMOCK_PORT` are injected by the runner and cannot be overridden.
Full targets run concurrently; selecting lite causes sequential execution.

For fixture edits, read [references/fixtures.md](references/fixtures.md).
`./mockstrow setup` validates fixtures and fetches the checksum-pinned WireMock jar.
It requires Java; Maestro, Android tools or Xcode and devices are separate prerequisites.
Use the CLI's prerequisite errors to identify missing tools; install them only within the
user's requested scope. Keep generated `.maestro/.runtime/` artifacts out of commits.
