# TUI

Run `./mockstrow` in a terminal.

| Tab | What it does |
| --- | --- |
| 1 Dashboard | devices, agent access (`a`), boot, stop, clear proxy; active runs across projects; sweep (`S`) |
| 2 Flows | filter, multi-select, run form with the equivalent CLI command |
| 3 Run | live workers and results; cancel, re-run failed |
| 4 History | past runs and logs; re-run failed, delete, prune |
| 5 Mock | serve a scenario, profile or flow on :18080, optionally attached to a device; live request log |
| 6 Stubs | a stub's own or merged JSON, its `extends` chain, and which flows use it |

## Keys

| Key | Action |
| --- | --- |
| `1`–`6`, `tab`, `h`/`l` | switch tab |
| `↑↓` / `jk`, `u`/`d`, `g`/`G` | move, page, top/bottom |
| `enter` / `f` | focus right pane |
| `/` | filter |
| `space` | select |
| `x` | stop / delete |
| `y` | copy |
| `r` | refresh |
| `e` | open in `$EDITOR` (`$VISUAL`, then `vi`); reloads on exit |
| `[` / `]` | walk the `extends` chain (Stubs) |
| `q` | quit |
