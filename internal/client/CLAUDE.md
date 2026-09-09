# internal/client — Daemon Client

Unix socket IPC client with RPC and event streaming. Single file, `client.go`; the sole interface between the TUI, CLI, MCP server and the daemon.

## Critical Invariants

- **All RPC methods are synchronous** — every daemon request (including `Subscribe`/`Unsubscribe`) waits on `respChan`. Fire-and-forget leaves the daemon's `MsgOK` reply orphaned in `respChan`, where it ambushes the next caller's response.
- **Background reader routes by message type** — broadcasts → `subChan`, others → `respChan`; wrong routing = hung RPCs or lost events
- **`Close()` uses `sync.Once`** — prevents double-close panics; do not add separate close paths
- **Reconnect is bounded to ONE retry per call** — on wire failure (Write error or `errConnectionClosed` signaled by `readLoop`), `sendAndWait`/`send` close the conn, dial fresh, transparently re-subscribe if previously subscribed, and retry the original request exactly once. A second consecutive failure escalates to the caller — never spin-loop. Reconnect does NOT fire after explicit `Close()` (the `done` channel is the terminal signal). See `reconnectLocked` for details.
- **Subscription state is tracked on the `Client` struct** — `c.subscribed` flips inside the `c.mu`-protected critical section via `sendAndWaitWithHook`'s onSuccess callback, so a concurrent reconnect can't miss the state change. The reconnect path re-issues `MsgSubscribe` on the new conn before retrying the original request, so broadcast-dependent flows (e.g. `create_task --wait_for_ready`) survive a daemon hiccup.

## Structure

- Lifecycle: `New(cfg)` (socket path from `cfg.Daemon.SocketPath`) → `Connect()` starts the reader goroutine → RPC calls → `Subscribe()` and read `Messages()` / `Errors()` → `Close()`.
- Internal helpers, lowest to highest: `send()` is fire-and-forget (rarely correct, see invariants); `sendAndWait()` / `sendAndWaitWithHook()` do one request-response with the bounded reconnect; `request()` additionally unwraps an error reply; `requestOK()` wraps `request()` for void RPCs.
- RPC methods are grouped by domain and mirror the daemon's `protocol.go`: agents, tasks, steps, tracks, workflows, dependencies/branches, routines, waits-on, and `Ping`. Add a method next to its group when adding a message type.
- The reader goroutine routes with `daemon.IsBroadcast(msg.Type)` (broadcasts → `subChan`, everything else → `respChan`). A new broadcast type must be added to `IsBroadcast` in `protocol.go` or it will land in `respChan` and corrupt the next RPC. `ParseAgentUpdate(msg)` decodes `agent_update`.
