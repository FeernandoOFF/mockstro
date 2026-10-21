# Devices

## Who may use what

Every run has an owner: `--owner human|agent|ci` (auto-detected).

- **People** may use any emulator or simulator.
- **Agents and CI** use only *designated* ones, in both lite and full integration.
- **Physical Android phones** are used only with `--map-remote` (lite, one device), or
  attached to a manual mock by a person (`mock --attach`). iPhones aren't supported.

## Designating emulators for agents

Any of these:

- `platforms.<platform>.devices` in `.maestro/mockstro.yaml` (shared) or `mockstro.local.yaml` (this machine)
- `~/.config/mockstro/designated.txt`, one name per line, for every project on this machine
- In the TUI Dashboard, select an emulator and press `a`. This edits the `devices` list and
  shows `agents: yes/no`. Names from `designated.txt` show `(global)` and are edited in that file.

Without any designated emulator, agent runs find no device.

## Parallel runs

Full integration only. Lite always uses one device.

- `--parallel 2`: up to two devices per platform, booting designated ones if needed.
- `--parallel auto`: every free running device; boots one if none are running.
- With both platforms on full, Android and iOS run at the same time.

Each worker gets its own WireMock port. Device and port locks are shared by every project
on the machine, so people, agents and CI never collide. `./mockstrow sweep` clears locks
left by crashed runs.

## Booting

- `./mockstrow boot [avd|simulator]` lists or boots a designated device.
- `run --boot-emulator` boots one when none is free; add `--headless` for no window.

## Machine-wide state

| Path | Contents |
| --- | --- |
| `~/.local/state/mockstro/locks` | device and port locks |
| `~/.local/state/mockstro/runs` | active runs (`./mockstrow status`) |
| `~/.cache/mockstro/` | pinned CLI and WireMock jars |
| `~/.config/mockstro/designated.txt` | machine-wide designated devices |
