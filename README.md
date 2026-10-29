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

Current release candidate: [v0.1.0-rc.6](https://github.com/FeernandoOFF/mockstro/releases/tag/v0.1.0-rc.6).
Prebuilt binaries support macOS and Linux on Apple Silicon/ARM64 and Intel/AMD64;
Go is not required.

There are two ways in. Developers and CI add mockstro to the app repo and run its
`./mockstrow` wrapper, which follows the version the project pins. QA and others who mostly
use the [browser UI](docs/web-ui.md) install one global CLI and a desktop shortcut.

### Add it to a project (developers)

From your app repo:

```bash
version=v0.1.0-rc.6
curl -fsSLo install.sh "https://github.com/FeernandoOFF/mockstro/releases/download/$version/install.sh"
sh install.sh "$version" && rm install.sh
```

This adds `./mockstrow` and pins the version in `.maestro/mockstro.yaml`. If that file
doesn't exist yet, it asks for your app IDs and creates a starter suite; existing
`.maestro` files are kept. You can pass them instead:
`sh install.sh "$version" --android-app-id com.example.app --ios-app-id com.example.ios`.

Commit `mockstrow` and `.maestro/`. Then anyone who clones the repo runs:

```bash
./mockstrow --version
./mockstrow check   # validates fixtures; finds/installs Java 17+; checks Maestro and WireMock
./mockstrow run     # runs flows tagged smoke
./mockstrow         # opens the TUI
```

You also need `adb` (Android) or Xcode (iOS), and an emulator or simulator. Java 17+
is found or installed automatically during installation and `check`.
`check` offers to install Maestro; use `check --install` to accept that in CI.
Maestro 2.11+ is recommended.

To upgrade an existing project, set the top-level pin in `.maestro/mockstro.yaml`:

```yaml
cliVersion: v0.1.0-rc.6
```

Then run `./mockstrow --version` and `./mockstrow check`; the wrapper downloads and
checksum-verifies the pinned binary automatically. Commit the updated pin. The project
installer is for adding a wrapper and refuses to overwrite an existing one.

Optional: `./mockstrow skill install --claude` adds an agent skill.

### Install the app (QA)

Once, on a Mac:

```bash
version=v0.1.0-rc.6
curl -fsSLo install-global.sh "https://github.com/FeernandoOFF/mockstro/releases/download/$version/install-global.sh"
sh install-global.sh "$version" && rm install-global.sh
```

This installs `mockstro` to `~/.local/bin` (`BIN_DIR` changes it) and creates
`~/Applications/Mockstro.app`. Double-click **Mockstro**, then paste the app repo's address
under **Clone from Git** (or choose a folder you already cloned); the page remembers it and
lists other projects you open. A project without `.maestro/mockstro.yaml` still opens if its
`.maestro` folder has flows or mocks; the page serves it as a [lite](#lite-or-full) project.
**Setup check** on the
page says what else is missing. Rerun these commands to update an existing global
installation. To check its version, run `~/.local/bin/mockstro --version` (or use your
custom `BIN_DIR`). On Linux, or for a CLI-only macOS install, use
`sh install-global.sh "$version" --no-launcher` before removing the installer.

Both installers look for a working Java 17+ on `PATH`, in `JAVA_HOME`, and in common
JDK locations (macOS JavaVirtualMachines, Homebrew, Linux JVM directories, Android
Studio, SDKMAN and jenv). If none works, setup runs `brew install openjdk@17`.
If Homebrew is unavailable or installation fails, setup stops with an error.
Mockstro finds the installed Java on future launches, including the QA desktop
shortcut, without editing shell profiles. Outside a project, `mockstro check`
checks Java and skips project checks.

The global CLI runs every project with its own version, not the project's pin; use a
version at least as new as the projects' `cliVersion`.

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
- [Web UI](docs/web-ui.md): `mockstro ui`, a browser front end for `mock`
- Integration: [overview](docs/integration.md), [Android](docs/android-integration.md), [iOS](docs/ios-integration.md)
- [Kotlin Toolchain](docs/kotlin-toolchain.md), [WebSockets](docs/websockets.md)
- [Contributing](CONTRIBUTING.md)
