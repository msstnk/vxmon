# AGENTS.md

## Project Summary
`vxmon` is a Linux terminal UI monitor for VXLAN, bridge, VRF, and network-namespace state. It targets infrastructure engineers who need fast, readable snapshots of kernel networking state across namespaces.

- Language: Go (`go 1.26.0`)
- TUI stack: Bubble Tea + Lip Gloss
- Kernel access:
  - `github.com/vishvananda/netlink`
  - `github.com/vishvananda/netlink/nl`
  - `github.com/vishvananda/netns`
- Entry point: `cmd/vxmon/vxmon.go`

The UI is split into two panes:
- Top pane: current context list for `Bridge`, `VRF`, or `NETNS`
- Bottom pane: detail table for the selected context

## Coding Principles
- **English Only**: Always use English.
- **Literal Comments Only**: Prohibit comments for self-explanatory code. Restrict comments to the physical meaning of magic numbers or hidden intentions that cannot be inferred from the code, such as syscalls or namespace operations.
- **Minimal Naming**: Keep variable names to the minimum length necessary to remain intelligible (e.g., r, ctx, mu).
- **Flat & Inline**: Prioritize conciseness over excessive abstraction for readability. Do not extract logic into a separate function if it is only referenced once; write it inline.
- **Clear Responsibility**: Do not duplicate boundary checks or validation. Execute them once at the responsible layer and assume valid input in all subsequent internal logic.
- **Standard Idioms**: Avoid creating custom utility functions. Generate the shortest implementation path by following Go standard library and package-specific best practices.
- **No Over-Engineering**: Prohibit interface definitions for "future extensibility." Always start with concrete types.

## Design Philosophy
- **Operational Intuition**: Prioritize immediate troubleshooting utility by transforming raw kernel data into a clear, actionable mental model.
- **Environment Agnostic**: Avoid assumptions about specific infrastructure layouts or container runtimes.
- **Graceful Degradation**: In non-privileged environments, suppress fatal errors and show the maximum information possible within permission limits.

## Repository Map
- `cmd/vxmon/vxmon.go`
  Starts the Bubble Tea program, configures debug/profiling, and connects store events to the app model.
- `internal/app/`
  Bubble Tea model, input handling, pane refresh logic, help overlay, and row builders for top/bottom panes.
- `internal/store/`
  Namespace discovery, netlink snapshots, runtime sampling, record reconciliation, and fade lifecycle management.
- `internal/ui/`
  Pane rendering, table formatting, palette helpers, and fade-aware styling.
- `internal/types/types.go`
  Shared domain structs passed between store and app layers.
- `internal/constants/constants.go`
  App title/version, tick intervals, fade timing, and common labels.
- `internal/debuglog/`
  Environment-driven file logging for debugging and profiling sessions.

## Runtime Model
1. `store.New()` initializes in-memory state and records the current network namespace.
2. `app.NewModel()` builds initial snapshots and render caches.
3. `store.Run()` starts the event loop and a namespace-resyncing netlink listener.
4. Netlink updates enqueue namespace reload requests with throttling.
5. The model performs targeted reloads and rebuilds only affected pane data where possible.
6. `clockTick` refreshes runtime/process/link-rate data on a slower cadence.
7. `animTick` advances transient fade metadata for recently added, updated, or removed rows.

## Main Data Sources
- Netlink:
  interfaces, FDB entries, neighbors, routes, and link statistics
- Procfs:
  namespace discovery fallback, per-namespace representative PIDs, socket counts, process CPU sampling
- Netlink attributes:
  bridge STP and bridge-port state (decoded from link attributes)

## UI Modes
Top-pane modes cycle with left/right while the top pane has focus:
- `BRIDGE`
  Bottom pane shows FDB records for the selected bridge.
- `VRF`
  Bottom pane can show route or neighbor tables for the selected VRF.
- `NETNS`
  Bottom pane can show per-namespace link stats or process lists.

Controls in `internal/app/model.go` and `internal/app/help_overlay.go`:
- `Tab`: switch focus between top and bottom panes
- `Left` / `Right`: change top mode or bottom table mode depending on focus
- `Up` / `Down`, `PgUp` / `PgDn`, `Home` / `End`: navigate lists
- `.` / `,`: move through child interfaces in bridge/VRF context
- `d`: toggle detailed view
- `t`: cycle top pane height
- `h` / `?`: open help
- `q` / `Ctrl+C`: quit

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

## Verification
Primary checks:
- `go build ./cmd/vxmon`
- `go test ./...`
- `staticcheck ./...`

## Environment Variables
- `VXMON_DEBUG`
  `off|error|info|trace` (also accepts `0`-`3`) to enable file logging to `debug.log`
- `VXMON_CPU_PROFILE`
  Value `1` writes a CPU profile to `cpu.pprof`

## Versioning
- `constants.AppTitle` reflects the current version and is displayed in the TUI title bar.

## Linux Assumptions
`vxmon` is Linux-specific and expects:
- netlink socket access
- `/proc` visibility for namespace and process sampling
- `unix.O_NOFOLLOW` support for log file handling
