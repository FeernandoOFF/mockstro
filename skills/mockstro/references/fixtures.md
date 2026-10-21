# Flows and mocks

Read the neighbouring flows, stubs and profiles first, and reuse their names and patterns.

## Flows

Flows live in `.maestro/flows/<feature>/`. Each has a YAML header, `---`, then Maestro steps:

```yaml
appId: ${APP_ID}
tags: [smoke, account]
properties:
  apiProfile: signed-in          # wiremock/profiles/signed-in.json, on top of shared.json
  apiOverrides: [account-empty]  # extra stubs; replace same-route stubs from the profile
  fbrcOverrides:                 # Remote Config values, always strings
    new_home_enabled: "true"
  apiVerifications:              # request counts checked after the flow
    - stub: account-empty
      atLeast: 1                 # or exactly / atMost
---
- launchApp
- assertVisible: "No accounts yet"
```

- Write `appId: ${APP_ID}` in every flow and subflow; the real IDs live in `mockstro.yaml`.
  mockstro sets `APP_ID` and `WIREMOCK_PORT` itself.
- Tags select flows. Branch per platform with Maestro's `runFlow` + `when: { platform: iOS }`,
  with platform subflows under `.maestro/shared/`.

## Mocks

- `wiremock/profiles/<name>.json` lists stub names: `{"mappings": [...]}`. `shared.json`
  always loads.
- `wiremock/stubs/*.json` are WireMock mappings. A stub may `extends` another stub or a
  template in `wiremock/bases/`. Only `response` is inherited; `request`, `priority` and
  scenario fields stay each stub's own. In a child, `"__remove__"` deletes an inherited key
  and `null` sets a JSON null.
- Bodies live in `wiremock/__files/`; use `bodyFileName` for shared or large bodies.
- `scenarios/*.yaml` are named mock setups for `./mockstrow mock`.

## Checking your work

- `./mockstrow resolve <stub>` prints a stub with its `extends` chain applied.
- `./mockstrow mock <profile|scenario>` serves a mock on port 18080 for manual inspection;
  attach it to a device only when asked (`--attach --device <serial>`).
