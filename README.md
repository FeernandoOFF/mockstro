<p align="center">
  <img src="docs/assets/monstruo.png" alt="Mockstro's friendly mint monster mascot holding a conductor's baton" width="220">
</p>

# Mockstro

**Deterministic mobile UI testing, orchestrated.**

Mockstro runs your [Maestro](https://maestro.mobile.dev) flows on Android emulators and
iOS simulators against a local [WireMock](https://wiremock.org). Each flow names the API
fixtures it needs; mockstro serves them, points the app at them, runs the flow and reports
the result. There's a CLI for scripts, agents and CI, and a TUI for people.

- **No live backend**: every flow gets its own mocked API.
- **Android and iOS**: one set of flows runs on both.
- **Parallel and safe**: runs by people, agents and CI share devices without colliding.

## Quickstart

From your app repo:

```bash
version=v0.1.0-rc.3
curl -fsSLo install.sh "https://github.com/FeernandoOFF/mockstro/releases/download/$version/install.sh"
sh install.sh "$version" && rm install.sh
```

This adds `./mockstrow` and pins the version in `.maestro/mockstro.yaml`. If that file
doesn't exist yet, it asks for your app IDs and creates a starter suite; existing
`.maestro` files are kept. You can pass them instead:
`sh install.sh "$version" --android-app-id com.example.app --ios-app-id com.example.ios`.

Commit `mockstrow` and `.maestro/`. Then anyone who clones the repo runs:

```bash
./mockstrow check   # validates fixtures; checks Java 17+, Maestro and WireMock (offers to install Maestro)
./mockstrow run     # runs flows tagged smoke
./mockstrow         # opens the TUI
```

You also need `adb` (Android) or Xcode (iOS), and an emulator or simulator. Java is
installed manually, e.g. `brew install --cask temurin@17`.

To change versions, edit `cliVersion` in `mockstro.yaml`. Optional: `./mockstrow skill install --claude`
adds an agent skill.

## Lite or full

Choose per platform:

| | Lite (default) | Full |
| --- | --- | --- |
| App changes | none | a debug-only E2E seam |
| Traffic to the mock | your Proxyman/Charles Map Remote | the app reads a per-run setting |
| Build | uses the installed app | mockstro builds and installs it |
| Devices | one at a time | many in parallel |

Start with lite to try it. Move to full for parallel runs and CI.
See [integration](docs/integration.md).

## Commands

```bash
./mockstrow run --tags smoke,regression --parallel auto
./mockstrow run .maestro/flows/account/overview.yaml --skip-build
./mockstrow run --platform ios
./mockstrow run --rerun-failed
./mockstrow mock <profile|scenario>       # serve a mock on :18080
./mockstrow list [profiles|scenarios]
./mockstrow status                        # runs, devices and ports on this machine
./mockstrow boot [avd|simulator]
./mockstrow sweep                         # clear locks left by crashed runs
```

`./mockstrow <command> --help` lists every flag.

## Docs

- [Configuration](docs/configuration.md): `mockstro.yaml`, local overrides, variables
- [Flows and fixtures](docs/fixtures.md): flow headers, stubs, profiles, recording
- [Devices](docs/devices.md): agent access, parallel runs, booting
- [CI and agents](docs/ci.md): reports, exit codes, result stages
- [TUI](docs/tui.md): tabs and keys
- Integration: [overview](docs/integration.md), [Android](docs/android-integration.md), [iOS](docs/ios-integration.md)
- [Kotlin Toolchain](docs/kotlin-toolchain.md), [WebSockets](docs/websockets.md)
- [Contributing](CONTRIBUTING.md)
