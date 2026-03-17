# AGENTS.md

## Project Summary
`vxmon` is a Linux terminal UI monitor for VXLAN, bridge, VRF, and network-namespace state, aimed at infrastructure engineers who work extensively with VXLAN and network namespaces.

- Language: Go (`go 1.26`)
- TUI stack: Bubble Tea + Lip Gloss
- Kernel access: `github.com/vishvananda/netlink` + `github.com/vishvananda/netns`
- Entry point: `cmd/vxmon/vxmon.go`

The UI is split into two panes:
- Top pane: current context list for `Bridge`, `VRF`, or `NETNS`
- Bottom pane: detail table for the selected context

## Repository Map
- `cmd/vxmon/vxmon.go`
  Starts the Bubble Tea program, configures debug/profiling, and connects netlink listeners to the app model.
- `internal/app/`
  Bubble Tea model, input handling, pane refresh logic, help overlay, and row builders for top/bottom panes.
- `internal/store/`
  Namespace discovery, snapshot collection, runtime sampling, record reconciliation, and fade lifecycle management.
- `internal/ui/`
  Generic pane rendering, table formatting, palette helpers, and fade-aware styling.
- `internal/types/types.go`
  Shared domain structs passed between store and app layers.
- `internal/constants/constants.go`
  App title, tick intervals, fade timing, and common labels.
- `internal/debuglog/`
  Environment-driven file logging for debugging and profiling sessions.

## Runtime Model
1. `store.New()` initializes in-memory state and records the current network namespace.
2. `app.NewModel()` calls `ReloadAll()` to build the initial snapshot and render caches.
3. `store.ListenNetlink()` discovers namespaces and subscribes to link, route, and neighbor updates per namespace.
4. Bubble Tea receives `NamespaceSyncMsg`, `NeighNLMsg`, `RouteNLMsg`, and `LinkNLMsg`.
5. The model performs targeted reloads and rebuilds only the affected pane data where possible.
6. `clockTick` refreshes runtime/process/link-rate data on a slower cadence.
7. `animTick` advances transient fade metadata for recently added, updated, or removed rows.

## Main Data Sources
- Netlink:
  interfaces, FDB entries, neighbors, routes, and link statistics
- Procfs:
  namespace discovery fallback, per-namespace representative PIDs, socket counts, process CPU sampling
- Sysfs:
  bridge STP state and bridge-port forwarding state for root namespace interfaces

## UI Modes
Top-pane modes cycle with left/right while the top pane has focus:
- `Bridge`
  Bottom pane shows FDB records for the selected bridge.
- `VRF`
  Bottom pane can show route or neighbor tables for the selected VRF.
- `NETNS`
  Bottom pane can show per-namespace link stats or process lists.

Important controls live in `internal/app/model.go` and `internal/app/help_overlay.go`:
- `Tab`: switch focus between top and bottom panes
- `Left` / `Right`: change top mode or bottom table mode depending on focus
- `Up` / `Down`, `PgUp` / `PgDn`, `Home` / `End`: navigate lists
- `.` / `,`: move through child interfaces in bridge/VRF context
- `d`: toggle detailed view
- `t`: cycle top pane height
- `h` / `?`: open help
- `q`: quit

## Performance Guidance
Hot paths:
- `internal/app/top_view.go`
- `internal/app/bottom_view.go`
- `internal/store/netlink_snapshot.go`
- `internal/store/runtime_snapshot.go`
- `internal/store/store.go::Advance`

When changing these areas:
- Avoid logging in render paths and animation-tick paths.
- Avoid repeated netlink calls when derived data can be reused.
- Prefer pre-sized slices/maps in snapshot and table builders.
- Keep per-tick allocations low, especially in `Advance`, `buildTopRows`, and `buildBottom`.
- Preserve targeted reload behavior; avoid turning namespace-local events into full refreshes without a reason.

## Editing Guidance
- Preserve current UI semantics unless the task explicitly asks for UX changes.
- Keep changes localized to the relevant package.
- Favor clear incremental improvements over broad refactors.
- When adding fields to shared structs in `internal/types/types.go`, trace all downstream builders and sort keys.
- Namespace access can fail due to permissions; degraded behavior should remain readable and non-fatal.
- Runtime data from `/proc` and `/sys` may be incomplete in containers or restricted CI environments.

## Verification
Primary checks:
- `go build ./cmd/vxmon`
- `go test ./...`

Manual validation is important for UI changes:
- run `go run ./cmd/vxmon`
- verify all three top modes
- verify bottom-mode switching for VRF and NETNS
- verify help overlay, focus switching, paging, and resize behavior
- if possible, validate live updates by creating/removing routes, neighbors, or links in a test namespace

## Environment Variables
- `VXMON_DEBUG`
  `off|error|info|trace` (also accepts `0`-`3`) to enable file logging to `debug.log`
- `VXMON_CPU_PROFILE`
  path for CPU profile output; value `1` writes to `cpu.pprof`

## Linux Assumptions
`vxmon` is Linux-specific and expects:
- netlink socket access
- `/proc` visibility for namespace and process sampling
- `/sys/class/net` for bridge metadata

For full visibility across namespaces, elevated privileges may be required.
