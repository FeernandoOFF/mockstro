# Browser UI

`mockstro ui` serves a page on `http://127.0.0.1:18090` for choosing a scenario, customer or flow
and starting the mock on port 18080, without remembering command names. It does what the TUI's
Mock tab does, for people who do not use a terminal.

A page left open across a restart reloads itself: every response carries the launch's name, and a
change of name makes the page reload once, picking up the new token.

It listens on loopback only. Every request must name that address in its `Host` header, and every
state-changing request must carry a token embedded in the page, so another website cannot drive the
mock.

## Starting it

- **Terminal:** `mockstro ui` inside a project opens that project. Elsewhere it reopens the last
  project it used, or shows a folder chooser. `--no-open` skips the browser and `--port` moves the
  page. If a UI is already running on the port, it just opens it.
- **Double-click:** `sh scripts/install-ui-launcher.sh [/path/to/mockstro]` creates
  `~/Applications/Mockstro.app`. It starts the UI with no terminal window and writes its output to
  `~/Library/Logs/mockstro-ui.log`. The page's **Quit** button stops the mock and the UI.

Recent projects are remembered in `ui.json` under `MOCKSTRO_STATE_DIR`, or under
`~/Library/Application Support/mockstro/` when that is unset.

## The page

A sidebar on the left, a top bar with breadcrumbs, and one view at a time. The page follows the
system's light or dark setting.

- **Sidebar:** **Session**, the **Library** (Mocks, Saved responses),
  **Setup check**, and a **Projects** tree. The tree lists the open project with its git branch and
  **Reload from disk** (after a `git pull`), plus recent projects to switch to. The footer shows
  whether the mock is running and has the **Quit** button.
- **Top bar:** breadcrumbs, the project's git branch as a chip, and its platform. The branch chip
  opens the **Version** dialog: the branch, its latest commit and whether it is up to date, with
  **Check for updates**, **Update to latest** and a branch list to **Switch** to. The page fetches
  once when a project opens; pulling and switching are always the tester's click.
- **Mocks:** everything that can be started, in one list grouped by the customer it is for:
  **Elec only**, **Gas only** and **Dual fuel**, plus **Other** for anything that fits none. Each
  group folds, and starts open only when it holds the running mock. Inside a group are the
  scenarios, then the bare customer profiles, then a folded **Automated tests** section holding the
  Maestro flows, split by feature folder. **Feature chips** (`survey`, `ces`, `usage`…) filter
  across every group, and search does too; filtering opens the groups that match. Starting an item
  serves it on port 18080 and opens the Session; choosing another while one is running switches
  without a restart. Which folds you left open is remembered in the browser.
  - The group comes from the customer profile's name: `dual…` is Dual fuel, `gas…` is Gas only and
    `elec…` is Elec only. A scenario or flow takes its profile's group.
  - Chips come from the `tags` in scenario and flow headers. A scenario also gets a feature from its
    Remote Config keys (`Config_<Feature>_…`) when a tag of that name exists somewhere in the
    project. `regression` and `smoke` mark test suites, and `electricity-only`, `gas-only` and `dual-fuel` name
    customer types, which the groups already show, so none of those appear as chips.
- **Session:** the running mock, with tabs for **Requests**, **Custom responses**, **Remote
  Config** and **Activity**, and a side panel with its details and the saved responses.
  - A request with no stub is red. Clicking a request shows what the app sent and the response it
    received.
  - **Remote Config** is editable. Each served value can be changed (true or false values get a
    toggle), **Removed** so the app gets no such key, or added, with suggestions from the keys the
    project's scenarios and flows already set. **Apply changes** serves the new values from the app's
    next Remote Config fetch, usually when it is relaunched; **Reset to scenario** puts the
    scenario's own values back. Only the Remote Config answer changes: the request list and custom
    responses stay. An edit lasts until the target is switched or the mock stopped.
  - **Edit response** (or **Add a response** on a red row) answers one route with a status,
    headers, body and delay you type. JSON is checked and errors name the line and column. It
    applies at once and lasts until the target is switched or the mock stopped.
  - **Save for later** writes the response as a normal stub at
    `.maestro/wiremock/stubs/local/<name>.json`, validated like every other stub and never
    overwriting an existing one. A saved stub is in no profile, so it is served only when applied
    from the page or passed on the command line: `mockstro mock <target> local/<name>`.
- **Setup check:** the checks below, and the Charles Map Remote rows a lite project needs, folded
  away until opened. An amber banner on the other views points here when something needs fixing.

## Setup check

The Setup check view checks, and says how to fix:

| Check | Passes when |
| --- | --- |
| Mock files | every stub, profile and scenario validates |
| Java | a JDK 17 or newer runs (macOS's placeholder `java` does not count) |
| Mock server program | the WireMock jar is cached (otherwise it downloads on first start) |
| Port 18080 | free, or in use by the mock this page started |
| Charles | the Charles process is running (lite projects) |
| Charles rules | Charles's saved Map Remote rules send the API host and Remote Config host to the mock; the first enabled matching rule decides, as in Charles |
| Mac proxy | the Mac's HTTPS proxy points at a local address |
| App traffic | the app has sent requests to the running mock |

The Charles rules check reads only the Map Remote section of
`~/Library/Preferences/com.xk72.charles.config`. Charles saves that file as rules change.

## Updating the checkout from the page

Pull and switch run `git` in the project's folder with these rules:

- A pull is `git pull --ff-only`. Nothing is merged, rebased, stashed, reset or discarded; if the
  branch cannot fast-forward the page says so and leaves the checkout alone.
- Both pull and switch refuse while a tracked file has local changes, naming the files. Untracked
  files, such as saved stubs under `stubs/local/`, never block them.
- Both refuse while a mock is running, because the files under it would change.
- Git never prompts. Anything that needs a sign-in fails with a plain message instead of hanging.
- Branch names are validated and passed as arguments, never through a shell.
