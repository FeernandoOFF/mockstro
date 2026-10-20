# Fixture edits

Inspect existing files before choosing names or adding a new abstraction.

- Flows in `.maestro/flows/` declare `appId: ${APP_ID}`, `tags` and mock properties in their YAML
  header, followed by `---` and Maestro steps. Put platform subflows under `shared/`
  and select them with native `runFlow.when.platform` conditions.
- `properties.apiProfile` selects `wiremock/profiles/<name>.json`. `shared.json` is always
  loaded. Profiles list stub names in a `mappings` array.
- `properties.apiOverrides` lists extra stubs, replacing same-route mappings from the
  profile. `fbrcOverrides` values are strings.
- `properties.apiVerifications` checks the request journal after the flow, using `stub`
  and `atLeast`, `exactly` or `atMost` counts.
- Stubs in `wiremock/stubs/` may `extends` another stub or a template from `wiremock/bases/`.
  Only `response` is inherited; request, priority and scenario fields belong to each stub.
  In a child response, `"__remove__"` deletes an inherited key and `null` sets a JSON null.
- Bodies live in `wiremock/__files/`; use `bodyFileName` for shared or substantial bodies.

Inspect effective mappings with `./mockstrow resolve <stub>`. Validate with
`./mockstrow setup`, then rerun the affected flow. For manual inspection,
`./mockstrow mock <profile-or-scenario>` serves on port 18080; attaching to a device is
an explicit additional action (`--attach --device`).
