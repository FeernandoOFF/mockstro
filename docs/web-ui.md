# Browser UI

`mockstro ui` serves a page on `http://127.0.0.1:18090` for choosing a scenario, profile or flow
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
- **Double-click:** the [global installer](../README.md#install-the-app-qa) creates
  `~/Applications/Mockstro.app`; `sh install-ui-launcher.sh [/path/to/mockstro]`, also a
  release asset, creates it for any other binary. It starts the UI with no terminal window and
  writes its output to `~/Library/Logs/mockstro-ui.log`. The page's **Quit** button stops the mock and the UI.

Recent projects are remembered in `ui.json` under `MOCKSTRO_STATE_DIR`, or under
`~/Library/Application Support/mockstro/` when that is unset.

## The page

A sidebar on the left, a top bar with breadcrumbs, and one view at a time. The page follows the
system's light or dark setting.

- **Sidebar:** **Session**, the **Library** (Profiles, Tests, Saved responses), and a **Projects**
  tree, with a small **Setup check** link above the footer. The tree lists the open project with its git branch and
  **Reload from disk** (after a `git pull`), plus recent projects to switch to. A project with an
  IntelliJ or Android Studio icon (`.idea/icon.svg` or `icon.png`, and the `_dark` variants in dark
  mode) shows it. The footer shows
  whether the mock is running and has the **Quit** button.
- **Top bar:** breadcrumbs, the project's git branch as a chip, and its platform. The branch chip
  opens the **Version** dialog: the branch, its latest commit and whether it is up to date, with
  **Check for updates**, **Update to latest** and a branch list to **Switch** to. The page fetches
  once when a project opens; pulling and switching are always the tester's click.
- **Tests:** the Maestro flows, grouped by the profile they use and split by feature folder inside
  each group. Each group folds, and starts open only when it holds the running test.
- **Profiles:** the profiles and scenarios, for exploring the app by hand. A group heading only
  shows when a group holds more than its own profile.
- On both, **tag chips** filter and search does too; filtering opens the groups that match. Starting
  an item serves it on port 18080 and opens the Session; choosing another while one is running
  switches without a restart. Which folds you left open is remembered in the browser, and so are
  each page's search and chip filter while the page stays open, so coming back from the Session
  does not mean searching again.
  - Clicking an item opens its **Preview**: what it would do, without starting it. Its **Start**
    button starts it straight away. A flow's test script and
    the checks it runs fill the page; **Responses it will serve** is a bar at the bottom: click it
    to open or close it, or drag its handle (or focus it and use the arrow keys) to make it taller
    or shorter; the height you leave it at is kept. It lists every API call the mock answers, grouped as added by the test, the profile's
    own, and the shared defaults (a mock without a script shows them as the page instead).
    **Details** opens a side panel with the mock's details and its Remote Config; it starts closed
    and marks itself when Remote Config has been changed. Both remember whether you left them
    open. Opening a call shows its response exactly as the app would get it (status, headers,
    body, resolved from the stub file and whatever it extends), with **Edit** on the call's row
    and **Copy** on the body. A
    call another stub already answers is dimmed, and a call the flow's `apiVerifications` count
    is marked.
  - A call that changes as the test runs shows as one call with an answer for each step, in order
    ("At the start", "After POST /api/topup"), and a line says what changes between them, field
    by field (`currentOverdueValueInEuro: 50 → 0`). The line follows any edits you make.
  - Both a call's response (**Edit**) and the Remote Config values can be changed before anything
    runs. **Start with your changes** serves the mock with the edited responses and values from
    the app's first request. Editing one step's answer leaves the others to change as the test
    runs: edit the answer at the start to 75 and the answer after the payment still arrives. A
    call that moves the test on (the payment itself) can't be edited, and a call that matches
    several URLs can't either; edit those from Requests once the app has made them. The edits are
    the same as the Session's custom responses: they show in that tab, with the step they apply
    to, and last until the mock is switched or stopped.
  - Groups use profile names by default; items without a profile appear under **Other**.
    Set `catalog.profileGroups` in the shared config to label or combine profiles and
    `catalog.groupOrder` to order labels. Unmapped profiles and new groups remain visible.
  - Chips use all explicit scenario and flow header `tags`, including suite tags such as
    `regression` and `smoke`. Optional `catalog.featureTags` maps exact Remote Config keys
    to additional tags when an item sets those keys. No key or profile naming convention
    is required. See [catalog metadata](configuration.md#web-catalog-metadata) for an example.
- **Session:** when nothing is running, it lists the last few things run in this project, each
  with **Preview** and **Start again**; an entry keeps the Remote Config values and edited responses it was
  started with. Otherwise it shows the running mock, with tabs for **Requests**, **Custom responses**, **Remote
  Config** and **Activity**, and a side panel with its details and the saved responses.
  - A request with no stub is red. Clicking a request shows what the app sent and the response it
    received.
  - **Test script:** serving a flow adds its steps and live checks to the side panel, which is
    wider and stays in view beside the Requests list. The steps are read from the flow file
    itself: taps, typed text, what to expect on screen, with the comment above each command as its
    explanation (a command's `label:` replaces the generated text). Branches for one platform get
    an **Android** / **iOS** switch, remembered in the browser. The script is a fold with its step
    count in the heading, so it can be folded away to leave the checks and Remote Config in view;
    whether it is open is remembered in the browser too. Waits, scrolling and the launch
    and utility sub-flows are left out. The **live checks** are the flow's `apiVerifications`,
    counted from the request list as the app calls the stubs. To run it again, stop the mock and
    start it again.
  - **Remote Config** is editable. Each served value can be changed (true or false values get a
    toggle), **Removed** so the app gets no such key, or added, with suggestions from the keys the
    project's scenarios and flows already set. **Apply changes** serves the new values from the app's
    next Remote Config fetch, usually when it is relaunched, and a line under the heading says when
    the app has fetched them; **Reset to scenario** puts the scenario's own values back. Only the
    Remote Config answer changes: the request list and custom responses stay. An edit lasts until
    the target is switched or the mock stopped. For a flow with a test script the editor is a fold
    in the side panel instead of a tab.
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
