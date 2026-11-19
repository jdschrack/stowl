# stowl

A terminal UI for looking after a local Docker environment, built with
[Bubble Tea](https://charm.land), Bubbles and Lip Gloss.

- **Containers** grouped by Compose project, with live CPU and memory figures, one
  line per port, a preview pane and btop-style CPU / memory graphs for whatever is
  selected.
- **Images, volumes and networks** with a "Used By" column showing which containers
  reference each one.
- **Logs** streamed into a popup with a severity gutter and highlighted timestamps,
  numbers, IPs, URLs, paths and levels.
- **Inspect** as readable sections rather than raw JSON.
- **Cleanup**: prune per tab, with disk usage in the tab bar.
- **System** tab: a menu of every prune docker offers, beside disk usage, engine
  info and a live feed of docker events.

## Install

Download a binary for your system from the
[releases page](https://github.com/jdschrack/stowl/releases), unpack it and put
`stowl` on your `PATH`. Or build it with Go:

```sh
go install github.com/jdschrack/stowl/cmd/stowl@latest
```

Needs the `docker` CLI on your `PATH`; stowl shells out to it, so it follows your
current Docker context. Project-level actions need the `docker compose` plugin.

## Keys

| Key | Action |
|---|---|
| `↑` `↓` / `j` `k` | move |
| `tab` / `←` `→` / `1`–`5` | switch tab |
| `/` | filter the list (`enter` to keep, `esc` to clear) |
| `i` | full inspect popup |
| `d` | delete, after a `y` confirmation |
| `p` | prune unused items on this tab, after a `y` confirmation |
| `q` | quit |

Containers tab:

| Key | Action |
|---|---|
| `s` | start or stop |
| `r` | restart |
| `l` | logs |
| `e` | shell (bash if present, else sh) |

System tab: `enter` runs the selected prune after a `y` confirmation, and `i` shows
the full disk usage and engine info.

On a project heading, `s`, `r`, `l` and `d` apply to the whole Compose project
(`d` runs `docker compose down`).

In the logs popup: `/` filters lines, `t` toggles timestamps, `g` / `G` jump to the
top / bottom, and scrolling up pauses following.

## Development

```sh
go test ./...
go build ./cmd/stowl
```

Pushing a `v*` tag builds the release binaries (`.github/workflows/release.yml`).

`cmd/stowl` is the entry point; everything else lives in `internal/tui`:

| File | Holds |
|---|---|
| `tui.go` | tabs, model, update loop and key handling |
| `view.go` | layout, table rows, panes, graphs and styles |
| `docker.go` | everything that runs the `docker` CLI |
| `detail.go` | inspect sections |
| `logs.go` | log streaming and highlighting |
