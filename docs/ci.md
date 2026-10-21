# CI and agents

## Reports

Each run writes to `.maestro/.runtime/runs/<sessionId>/`:

- `result.tsv`, `junit.xml`, `run.json`, and a Maestro log per flow
- `progress.tsv`, updated as flows finish

`--junit <path>` writes an extra copy of the JUnit report. The newest
`MOCKSTRO_KEEP_RUNS` runs (default 20) are kept.

## Exit codes

| Command | Code | Meaning |
| --- | --- | --- |
| `run` | 0 | every flow passed |
| | 1 | a flow failed |
| | 130 | interrupted |
| `status --quiet` | 0 | idle |
| | 10 | runs active, a device is free |
| | 11 | runs active, nothing free |

`status --json` gives the full picture.

## Result stages

| Stage | Meaning |
| --- | --- |
| `PASS` | passed |
| `TEST` / `RECORD` | Maestro failed |
| `VERIFY` | passed, but an `apiVerifications` count didn't match |
| `SETUP` | setup failed before the flow ran |
| `OVERRIDE` | the app never confirmed it read the run's mock (full only) |
| `CANCELLED` | stopped by the user |
| `INTERRUPTED` | the run ended during the flow |
| `UNSTARTED` | the run ended before the flow started |

## Batching

One Maestro process runs several flows per device, so driver startup is paid once.
Each flow still gets its own mock. Use `--isolate` for one process per flow when a
failure might come from an earlier flow.

Agents and CI only use designated devices, see [devices](devices.md).
