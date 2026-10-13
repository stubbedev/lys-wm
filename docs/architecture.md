# Architecture

The decisions every change builds on. Scope and non-goals are in the
[README](../README.md); the plan is in the issues. When a decision changes,
change it here in the same commit and say why in the issue that changed it.

## Pure Go

tilde builds with `CGO_ENABLED=0`, always. A C library is used only when no
reasonable Go alternative exists, and then it is loaded at runtime with
[purego](https://github.com/ebitengine/purego) (`dlopen`), never linked.

| Need | Source |
| --- | --- |
| EGL, GLES2, GBM | `libEGL`, `libGLESv2`, `libgbm` through purego (#33) |
| Input devices | `libinput` through purego (#42) |
| Wayland wire protocol | in-repo codec and scanner (#3, #4) |
| DRM/KMS ioctls, epoll, timerfd, signalfd, memfd | [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix) |
| logind | [`github.com/godbus/dbus/v5`](https://github.com/godbus/dbus) |
| udev | netlink, read directly (#39) |
| Keymaps | [`github.com/unxed/xkb-go`](https://github.com/unxed/xkb-go) |
| X11 window manager | [`github.com/jezek/xgb`](https://github.com/jezek/xgb) (#45) |
| Text | [`github.com/go-text/typesetting`](https://github.com/go-text/typesetting), through gelm's `render` |

Adding a purego library, or a dependency to this table, is a decision issue.

## Threading

One goroutine owns all compositor state and runs the event loop (#5). Other
goroutines exist only at the edges, for blocking work such as D-Bus. They
post closures to the loop's mailbox and never read or write state
themselves. Nothing in the compositor takes a lock to protect its state.

## Pure core

`internal/wm` (#21) knows nothing about Wayland, X11 or rendering. It is a
value-level model of the container tree, workspaces and focus, with
functions from state and event to new state and effects. It imports only
the standard library. The protocol glue translates both ways: client
requests and backend input into `wm` events, and `wm` effects into protocol
events and render state.

## Dispatch

One typed `Action` set and one dispatcher, in `wm` (#66). Keybinds,
`tildectl dispatch`, the IPC socket and protocol requests such as
foreign-toplevel activate are all callers of it. Nothing else mutates WM
state.

## Packages

Everything lives under `internal/` except the commands.

| Package | Owns |
| --- | --- |
| `cmd/tilde` | The compositor binary: builds the loop, backends, renderer and server, and runs them |
| `cmd/tildectl` | The IPC client (#31) |
| `wire` | The message codec and fd passing (#3); server and client side |
| `scanner` | Generates `protocol/...` from protocol XML (#4) |
| `protocol/...` | Generated bindings, one package per protocol; never edited by hand |
| `loop` | The event loop: fd sources, timers, signals, idle queue, mailbox (#5) |
| `server` | Display socket, clients, object maps, globals (#6) |
| `backend` | The backend interface: outputs, frames, input devices (#8) |
| `backend/headless` | Virtual outputs and injected input, for tests (#8) |
| `backend/nested` | Outputs as windows on a parent Wayland compositor (#9) |
| `backend/drm` | Atomic KMS on one DRM device (#40) |
| `render` | The renderer interface (#7) |
| `render/soft` | The CPU renderer, for headless and golden tests (#7) |
| `render/gles` | The GLES2 renderer (#34) |
| `gpu` | purego bindings for EGL, GLES2 and GBM (#33) |
| `seat` | `wl_seat`, keyboard, pointer and touch (#13, #14) |
| `xkb` | Keymap compilation and state (#13) |
| `shell` | xdg-shell and layer-shell glue between clients and `wm` (#15, #48) |
| `wm` | The pure core and the dispatcher (#21, #66) |
| `xwm` | The X11 window manager for XWayland (#45) |
| `config` | The typed config, its validation and reload (#30) |
| `ipc` | The IPC socket and event stream (#31) |
| `session` | logind, udev and libinput: device access, hotplug and VT switching (#38, #39, #42) |

Dependencies point inward: `wm` imports nothing from tilde; `wire` and
`loop` import nothing from tilde; `protocol/...` imports only `wire`;
backends and renderers do not import `server`, `shell` or `wm`. `cmd/tilde`
is the only place that knows every package.

## Devices

Nothing assumes a single GPU. Each DRM device owns its own `backend/drm`
instance and its own renderer. The primary render device is an explicit
choice passed to whatever needs it, never an implicit global. Multi-GPU
itself lands in M9 (#62), which records the selection rule here.

## Testing

- Unit tests for the pure core and every package with logic of its own.
- Golden images, byte-exact, from the software renderer.
- The compositor-in-the-loop harness (#10) boots tilde headless with real
  clients. It is the integration gate.

## Reference implementations

Hyprland no longer uses wlroots.
[aquamarine](https://github.com/hyprwm/aquamarine) is its backend library
(DRM/KMS, session, libinput, GBM, headless and nested backends) and
[hyprwayland-scanner](https://github.com/hyprwm/hyprwayland-scanner)
generates its protocol bindings; the wire format still comes from
libwayland-server. Hyprland's `hyprtester` is its compositor-in-the-loop
suite and the reference for #10. Issues link the relevant files at pinned
commits under a **Reference** heading.

Licences decide how they may be used:

- Hyprland, aquamarine and hyprwayland-scanner are BSD-3-Clause, libwayland
  is MIT. Their logic may be ported; ported code keeps the original
  copyright notice and licence in `THIRD_PARTY.md`.
- hy3 is GPL-3.0. Read it to pin down behaviour (collapse policy, autotile,
  move semantics); never port its code.
