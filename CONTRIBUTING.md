# Contributing

Needs Go 1.25.6+.

```bash
make test        # engine checks + TUI render tests
make vet
make install     # installs mockstro from source
```

## Trying a dev build in an app

```bash
make install-dev                    # installs ~/.local/bin/mockstro-dev
cd /path/to/your/app
./mockstrow --dev run --tags smoke  # uses mockstro-dev instead of the pinned release
```

- `--dev` must come first. It skips the release download and leaves the pin unchanged.
- To use a build without installing it: `make build-dev`, then
  `MOCKSTRO_DEV_CLI=/path/to/mockstro/bin/mockstro-dev ./mockstrow --dev run`.
- Rerun `make install-dev` after changing the source. `make uninstall-dev` removes it;
  `BIN_DIR` changes the install directory.

## Releasing

Push a tag such as `v0.2.0` or `v0.2.0-rc.1`. The release workflow tests, builds the
macOS/Linux binaries, `mockstrow`, the install scripts and `checksums.txt`, and publishes a
GitHub release (a prerelease for `-rc` tags). `scripts/build-release.sh vX.Y.Z` builds
the same files locally into `dist/`.

The skill is embedded from `skills/mockstro/`; the wrapper is `internal/bootstrap/mockstrow`.
Checksums catch corrupted downloads; they aren't signatures.
