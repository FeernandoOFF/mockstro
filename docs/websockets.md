# WebSocket fixtures

HTTP and native WebSocket mocking use the same checksum-pinned WireMock
`4.0.0-beta.41`. Configure the target in
`.maestro/mockstro.yaml`:

```yaml
platforms:
  android:
    appId: dev.example.android
    integration: full
    websocketPath: /ws
```

Java 17 or newer is required. `websocketPath` is optional for
server-only mocking; it controls the endpoint passed to apps in full integration.

## Fixtures and profiles

Keep native WireMock JSON in `wiremock/stubs/`. A profile's existing `mappings` list may
contain HTTP fixtures (`request` / `response`) and message fixtures (`trigger` / `actions`).
Names must match their relative file paths without `.json`, including directory names.

First define `stubs/ws/upgrade.json`:

```json
{
  "name": "ws/upgrade",
  "request": { "method": "GET", "url": "/ws" },
  "response": { "openWebsocketChannel": true }
}
```

The published beta requires this HTTP upgrade fixture. A message fixture alone returns 404
when the app attempts to connect.

For Kairos, `stubs/ws/connect.json` can respond to its JSON connection request:

```json
{
  "name": "ws/connect",
  "trigger": {
    "type": "message",
    "channel": {
      "type": "websocket",
      "initiatingRequestPattern": { "url": "/ws" }
    },
    "message": {
      "body": {
        "matchesJsonPath": { "expression": "$.method", "equalTo": "connect" }
      }
    }
  },
  "actions": [{
    "type": "send",
    "channelTarget": { "type": "originating" },
    "message": {
      "body": {
        "data": "{\"type\":\"res\",\"id\":\"{{jsonPath message.body '$.id'}}\",\"ok\":true,\"payload\":{\"token\":\"e2e-token\",\"serverName\":\"E2E Server\",\"management\":false}}"
      }
    }
  }]
}
```

The response ID must match the incoming request ID. Create separate fixtures for presence,
Agent lists, Session data and other feature requests. Unit results use `"payload":{}`.
Server events use Kairos's `type`, `event`, `payload` and `seq` envelope. Fixtures should
accept both first-time pairing and subsequent connections with the mock-issued token.

A profile can reference both fixtures:

```json
{ "mappings": ["ws/upgrade", "ws/connect", "ws/presence", "ws/agents"] }
```

Overrides retain the current priority layers. Message identities use the complete trigger,
with JSON object keys normalized; different messages on `/ws` remain distinct. Message
fixtures do not support `extends`. Send bodies may use inline `data` or a `filePath` relative
to `wiremock/__files/`; binary bodies use native WireMock `encoding`.

## Verification and isolation

`mockstro.apiVerifications` work for either fixture kind:

```yaml
mockstro:
  apiProfile: chat
  apiVerifications:
    - stub: ws/connect
      atLeast: 1
    - stub: ws/chat-send
      exactly: 1
```

Message counts include only received events matching the named stub. Outgoing replies and
pushes do not inflate them. Unmatched incoming messages appear in diagnostics. The TUI
traffic journal shows HTTP requests and incoming/outgoing WebSocket messages; the stub
panel shows native message actions.

Before a WebSocket scenario, mockstro stops its attached app, loads both fixture kinds and
resets both journals, then writes the override. On every scenario switch, it closes the
previous message channels and resets HTTP and message mappings and journals without
restarting the JVM. The next Maestro flow must launch the app. Each worker keeps its own
port and server. Server-only clients must reconnect when their scenario is switched.

## Controlled server pushes

Copy `examples/websockets/send-message.js` into `.maestro/shared/`. `runScript` uses the
existing host-side `WIREMOCK_PORT` variable. After the app is connected and subscribed:

```yaml
- assertVisible: "Waiting for approval"
- runScript:
    file: ../../shared/send-message.js
    env:
      WS_PATH: /ws
      WS_MESSAGE: '{"type":"event","event":"agents.changed","payload":[],"seq":1}'
```

The helper requires an active channel and posts to `/__admin/channels/send`. A successful
send verifies transport delivery, not UI consumption; follow it with a UI assertion. For
streaming, send each protocol event in order after a subscription is established. Use UI or
journal readiness checks instead of arbitrary sleeps.

## App integration

Android's schema-1 override gains optional `websocketUrl`. iOS's bundle configuration gains
`e2eWebsocketUrl`, and the simulator environment gains `E2E_WEBSOCKET_URL`. Existing HTTP
fields are preserved. Cleanup removes the new iOS environment key too, and preflight checks
that WebSocket endpoints round-trip. Acceptance markers are cleared before each WebSocket
override so a previous flow cannot satisfy the next flow's acceptance check.

The app must read this in its debug/test build, check expiry and run identity, and publish
acceptance using the existing platform mechanisms. Configure a regular remote Server using
the WebSocket URL; keep the real network client and feature logic. Kairos additionally needs
its platform DI to select that remote Server in test mode and keep test preferences and
credentials separate from ordinary app storage.

## Real transport regression

```sh
MOCKSTRO_TEST_WIREMOCK_JAR=/path/to/wiremock-standalone-4.0.0-beta.41.jar go test ./internal/wiremock -v
```

This test validates the checksum, starts an isolated loopback server, and uses Java's
standard WebSocket client to exercise upgrade, request-ID templating, server pushes,
message journaling and scenario reset. Without the variable, ordinary tests still cover
fixture validation, journal routing and ownership; the real transport test is skipped.

References: [WireMock message stubbing](https://wiremock.org/docs/messaging/stubbing/),
[verification](https://wiremock.org/docs/messaging/verification/),
[Maestro HTTP scripts](https://docs.maestro.dev/maestro-flows/javascript/make-http-requests).
