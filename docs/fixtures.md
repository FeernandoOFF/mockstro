# Flows and fixtures

## Layout

```
.maestro/
├── config.yaml
├── mockstro.yaml             # see configuration.md
├── mockstro.local.yaml       # ignored, per machine
├── flows/<feature>/*.yaml    # Maestro flows
├── shared/                   # sub-flows used with runFlow
├── scenarios/*.yaml          # named mock setups for `mock`
└── wiremock/
    ├── stubs/*.json          # WireMock mappings
    ├── bases/*.json          # templates stubs can extend
    ├── __files/              # response bodies (bodyFileName)
    └── profiles/*.json       # {"mappings": [stub names]}; shared.json always loads
```

## Flow header

```yaml
appId: ${APP_ID}
tags: [smoke, account]
properties:
  apiProfile: signed-in          # profiles/signed-in.json, on top of shared.json
  apiOverrides: [account-empty]  # extra stubs; replace same-route stubs from the profile
  fbrcOverrides:                 # Remote Config values (strings)
    new_home_enabled: "true"
  apiVerifications:              # checked after the flow
    - stub: account-empty
      atLeast: 1                 # or exactly / atMost
---
- launchApp
- assertVisible: "No accounts yet"
```

Remote Config uses the same validation for fixture files, CLI overrides and live edits:
at most 500 values, keys of 1–200 ASCII letters, digits, `_`, `.` or `-`, and values
up to 64 KiB each. CLI overrides replace matching fixture values; omitted keys use
app defaults. Attached full-integration runs deliver values through the device override;
server-only and Map Remote sessions serve them through WireMock's Firebase mappings.

## Stubs

- A stub can `extends` another stub or a base. Only `response` is inherited.
- In a child, `"__remove__"` deletes an inherited key and `null` sets it to null.
- `./mockstrow resolve <stub>` shows the merged result.

## Trying mocks by hand

```bash
./mockstrow mock <profile|scenario>                        # serve on :18080
./mockstrow mock <scenario> --attach --device emulator-5554
```

## Recording real traffic

`./mockstrow record-api --target https://api.example.com` starts a recording proxy. Point
the app at the printed address, then press Ctrl-C to save mappings under
`.maestro/.runtime/wiremock/recordings/`. Only `/api/.*` is captured by default; use
`--url-path-pattern '/.*'` for everything. `./mockstrow playback <dir>` serves a recording.
