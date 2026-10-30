# Cluster sessions

`radar-sessions` runs one Radar per kubeconfig context behind one loopback
address, so each browser tab can stay on its own cluster. Switching cluster in
one tab never moves another tab.

```bash
make build sessions
./radar-sessions                      # opens the current-context
./radar-sessions --context prod-eu    # opens another context first
./radar-sessions -- --disable-exec    # extra flags for every session's Radar
```

Open `http://localhost:9300/_sessions/` to list, start and stop sessions. Inside
Radar, the layers button in the header does the same: click a cluster to move
this tab to it, Cmd/Ctrl-click to open it in a new tab, the square to stop it.

## How it works

Each session is an unmodified Radar process started as:

```
radar --kubeconfig <session>/kubeconfig --port <free port> --listen-address 127.0.0.1 \
      --base-path /s/<slug> --no-browser --no-version-check
RADAR_STATE_DIR=<session>/state
```

The controller on `127.0.0.1:9300` forwards `/s/<slug>/…` to that process and
does nothing else with the traffic. Radar serves its own UI and loading screen,
so a cluster that is slow to connect shows Radar's normal progress, never an
error page.

| What keeps sessions apart | Why it holds |
|---|---|
| Separate process per cluster | Its own memory, caches, watches and connection |
| `kubeconfig` with only that context, its cluster and user (0600) | The session has no other context to switch to, from any part of the UI or API |
| `RADAR_STATE_DIR` per session | Settings, AI history, What's New and the MCP port file are never shared. Starting a session never marks another session's AI runs stale or rewrites `~/.radar/mcp-port` |
| Routing by the first path segment only | `/s/<slug>/` always reaches that slug's process; unknown slugs are 404, never a fallback |
| `Host` preserved, no forwarding headers | Radar's own same-origin and CSRF checks run unchanged |

A new session's state directory is seeded once from `~/.radar` with
`config.json`, `settings.json`, `clusters.json`, `install-id`,
`whats-new.json` and `star.json`. AI history, timeline, MCP port and usage
report files are never copied.

## Lifecycle

- **Stop** sends SIGTERM, waits 15 s, then kills the session's process group.
  The pinned kubeconfig is deleted when the process exits.
- **A stopped session stays stopped.** Requests from an open tab get
  `503 session_stopped`; only the Start button (status page or menu) starts it.
- **Controller exit** (Ctrl+C, SIGTERM, SIGHUP) stops every session.
- **Controller crash or SIGKILL**: each session runs under a guard
  (`radar-sessions guard`) that polls its parent and ends the session when the
  controller is gone. A later controller also ends any session recorded in
  `<home>/<slug>/session.json` that is still running.
- Running `radar-sessions` again while one is up opens the running one.

## Security

- Listens on `127.0.0.1` only and refuses any `Host` that is not a loopback
  name (DNS rebinding).
- Writes need the `X-Radar-Sessions: 1` header and a same-origin request;
  reads refuse `Sec-Fetch-Site: cross-site` and `same-site`.
- The controller's own pages use a nonce-based CSP with no inline exceptions.
- Flags the controller owns (`--port`, `--kubeconfig`, `--kubeconfig-dir`,
  `--listen-address`, `--base-path`, `--no-browser`) cannot be passed through.

## Limits

- All sessions share one browser origin. Browser storage that Radar keys without
  the cluster (table column settings, the theme) is shared, which is usually
  what you want. A single tab that moves between clusters keeps its
  `sessionStorage`, so an unsaved YAML draft for a same-named resource could
  reappear on the other cluster.
- Every running session is a full Radar. The menu and the manager page show
  each one's memory; stop the ones you are not using.
- `radar diagnose` and MCP clients that read `~/.radar/mcp-port` find your
  normal Radar, not a session. Each session's MCP endpoint is
  `http://localhost:9300/s/<slug>/mcp`.
