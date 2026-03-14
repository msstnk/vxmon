# AGENTS.md

## Project Overview
`vxmon` is a terminal UI network monitor for Linux.

- Language: Go (`go 1.26`)
- UI stack: Bubble Tea + Lip Gloss
- Data source: `vishvananda/netlink`
- Entry point: `cmd/vxmon/vxmon.go`

The app renders two panes:
- Top pane: Bridge or VRF summary and interface context
- Bottom pane: FDB / Neigh / Route tables for the selected context

## High-Level Architecture
- `cmd/vxmon/vxmon.go`
: starts Bubble Tea program, initializes store/model, subscribes to netlink updates.
- `internal/store/*`
: snapshot collection (`netlink_snapshot.go`), diff/reconcile state transitions (`reconcile.go`), and fade-state lifecycle (`store.go`).
- `internal/app/*`
: Bubble Tea model/update/view and per-pane row builders.
- `internal/ui/*`
: generic table formatting, panes, and fade color helpers.
- `internal/types/types.go`
: shared data structures.

## Runtime Flow
1. Store loads initial snapshot (`ReloadAll`).
2. Netlink listener sends events (`NeighNLMsg`, `RouteNLMsg`, `LinkNLMsg`) to the Bubble Tea loop.
3. Model handles events, triggers targeted reloads, and rebuilds view rows.
4. `animTick` advances fade state; `clockTick` updates header time.

## Performance Notes
These paths are hot and should be treated carefully:
- `internal/app/top_view.go` and `internal/app/bottom_view.go`
: can run often due to ticks and netlink updates.
- `internal/store/netlink_snapshot.go`
: netlink/sysfs reads are relatively expensive.
- `internal/store/store.go::Advance`
: called every animation tick.

Guidelines:
- Avoid logging inside render/build paths.
- Avoid per-tick allocations in store/update loops.
- Prefer capacity hints for slices/maps in snapshot and table builders.
- Keep netlink calls minimal and avoid redundant lookups.

## Build and Run
- Run app: `go run ./cmd/vxmon`
- Build binary: `go build ./cmd/vxmon`
- Run tests: `go test ./...`

## Agent Working Rules
- Preserve current UI behavior unless the task explicitly requests UX changes.
- For performance changes, prioritize low-risk reductions in allocations and redundant IO.
- Keep changes localized to relevant package(s); avoid broad refactors without request.
- Always run `gofmt -w` on edited Go files.
- If available, run `go test ./...` after changes and report failures clearly.

## Linux Assumptions
The monitor expects Linux networking primitives:
- netlink socket access
- `/sys/class/net` availability for bridge/port state

On restricted environments (containers/CI), some runtime data may be incomplete.
