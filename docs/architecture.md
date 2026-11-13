# Architecture

The decisions every change builds on. Scope and non-goals are in
[Scope](./guide/scope). When a decision changes, change it here in the same
commit.

## Pure Go

lys-wm builds with `CGO_ENABLED=0`, always. A C library is used only when no
reasonable Go alternative exists, and then it is loaded at runtime with
[purego](https://github.com/ebitengine/purego) (`dlopen`), never linked.

| Need | Source |
| --- | --- |
| EGL, GLES2, GBM | `libEGL.so.1`, `libGLESv2.so.2`, `libgbm.so.1` through purego (`internal/gpu`) |
| Vulkan | `libvulkan.so.1` through lys-lib's `third_party/vk` and `gpu` |
| Input devices | `libinput.so.10` through purego (`internal/session`) |
| Keymaps and keyboard state | lys-lib's `xkb`, pure Go |
| Wayland wire protocol | in-repo codec (`wire`) and generator (`scanner`) |
| DRM/KMS ioctls, pixel formats, EDID | lys-lib's `drm`, `fourcc`, `edid` |
| epoll, memfd | `golang.org/x/sys/unix` |
| logind, systemd, D-Bus | lys-lib's `third_party/godbus` |
| udev | netlink, read directly |
| X11 window manager | `github.com/jezek/xgb`, built from `third_party/xgb` (a `go.mod` replace) |
| Text | lys-lib's `render` on its `third_party/typesetting` |
| Icon themes | lys-lib's `icontheme` |
| Config file | `go.yaml.in/yaml/v3`, strict |

Adding a purego library or a dependency to this table is a decision.

- The Nix build bakes the purego libraries' store paths in; release builds load
  them by soname. `LYS_WM_LIBEGL`, `LYS_WM_LIBGLESV2`, `LYS_WM_LIBGBM`, `LYS_WM_LIBINPUT` and
  `LYS_LIBVULKAN` override them.
- Without cgo, `os.Setenv` does not reach libc's `getenv`: anything a driver
  reads must be in the process environment at start.
- Keymaps compile in the xkb v2 format; clients receive xkb_v1 text, as
  `wl_keyboard` carries. `TestLibxkbcommonClientsReadAllThreeLayouts` holds the
  output to libxkbcommon's `xkbcli`.
- lys-wm discovers no fonts. The tab font is `tabs.font`, else `LYS_WM_FONT`,
  else the DejaVu Sans the build bakes in.

## Packages

Everything lives under `internal/` except the commands.

| Package | Owns |
| --- | --- |
| `cmd/lys-wm` | The compositor binary: picks backend, renderer and primary GPU, builds the loop and compositor, the holder process, thread priority |
| `cmd/lys-wmctl` | The IPC client |
| `cmd/lys-wm-pace` | Boots lys-wm with its frame trace on and reports how smoothly each animation reached the screen ([Frame pacing](./frame-pacing)) |
| `compositor` | Connects backend events to the server, places windows, draws each output's frames; `cmd/lys-wm` and `harness` both build on it |
| `wm` | The pure core: container tree, workspaces, focus, rules, layout and the action dispatcher |
| `anim` | Easings, springs, tweens and the per-event animation table; pure |
| `binds` | Typed chords (keycode from the first layout, button or scroll direction), submaps and bind flags, matched to `wm` actions |
| `config` | The typed config, its validation and reload |
| `loop` | The event loop: fd sources, timers, signals, idle queue, mailbox; a manual clock for tests |
| `wire` | Message codec, fd passing, the object runtime generated bindings build on |
| `scanner` | Generates `protocol/{server,client}/...` from the XML pinned in `protocol/protocols.json` |
| `protocol/...` | Generated bindings, one package per protocol and side; never edited by hand |
| `server` | Display socket, clients, object maps, globals, sandbox listeners, the handover journal |
| `wlclient`, `wlclient/feedback` | The client side of a connection, for the nested backend and tests; reading dmabuf feedback |
| `backend` | The backend interface: outputs, frames, input devices, error classes |
| `backend/drm` | Atomic KMS on one DRM device |
| `backend/nested` | Outputs as windows on a host Wayland compositor |
| `backend/headless` | Virtual outputs rendered in software and injected input, for tests |
| `backend/virtual` | An in-memory output on any backend, paced by its own clock, never presented |
| `render` | The renderer interface |
| `render/soft`, `render/gles`, `render/vulkan` | CPU, GLES2 and Vulkan (lys-lib `gpu`) renderers |
| `render/rendertest` | The scene and checks every renderer is held to the software renderer's pixels with |
| `gpu` | purego bindings for EGL, GLES2 and GBM; `PinThread` |
| `shader` | The screen-shader language: GLSL ES 1.00 subset to GLSL, SPIR-V and a Go program |
| `colorspace` | Transfer functions, primaries, ICC profiles, transforms between encodings; pure |
| `region` | Pixel regions as disjoint rectangles |
| `surface` | `wl_surface` state, content-update queues, subsurface trees, roles |
| `shell`, `seat`, `data`, `output`, `dmabuf`, ... | One package per Wayland protocol family, named after it; its package comment says what it implements |
| `xkb` | Keymaps through lys-lib's `xkb`, served as sealed memfds |
| `xwayland`, `xsync` | Xwayland, the X11 window manager, selection and Xdnd bridges; X SYNC counters for `_NET_WM_SYNC_REQUEST` |
| `session` | logind, udev, libinput: device access, hotplug, VT switching, the lid state and inhibitor, AC state; the holder protocol (`remote.go`) |
| `tabletinput` | Tablet tools and pads from libinput to the compositor |
| `handover` | Moves kernel objects and state to the next lys-wm process |
| `ipc` | The IPC socket and event stream |
| `systemd` | Hands the environment to D-Bus activation and the user manager; runs `lys-wm-session.target` |
| `powerprofile`, `rtkit` | power-profiles-daemon's active profile; RealtimeKit priority requests |
| `frametrace` | JSON-lines record of each composited frame's cost and presentation |
| `harness`, `testclient`, `testenv`, `golden` | Compositor-in-the-loop tests, a scripted client, skip-or-fail policy for missing tools, golden images |

Import rules:

- `wire`, `loop` and `region` import nothing from lys-wm.
- `wm` imports only `anim` and `render`'s value types; `anim` imports nothing
  from lys-wm.
- `protocol/...` imports only `wire` and other protocol packages of the same
  side.
- Backends and renderers never import `server` or `shell`. Backends import
  `wm` only for shared value types (`wm.ContentType`).

## Threading

One goroutine owns all compositor state and runs the event loop. Other
goroutines exist only at the edges, for work that blocks: they post closures
to the loop's mailbox and never read or write state themselves. A goroutine
that only hands back a result uses `Loop.Hand`: a closed loop is the shutdown
race and drops the closure, any other failure to wake the loop panics. One that
must clean up or answer when the loop is gone uses `Loop.Post` and handles its
error, where `loop.ErrClosed` alone means the loop has shut down.
Compositor state takes no locks. Packages under it that keep their own are the
loop's mailbox, the IPC server's connection set, the input queue in `session`,
and process-wide tables in `gpu` and `session`.

Input has a thread of its own. `session.Libinput` runs libinput on a goroutine
locked to one OS thread, with its own `loop.Loop`. libinput is not thread-safe,
so every libinput call happens there: context creation, dispatch, reading
events, adding and removing devices, suspend and resume, device settings,
keyboard LEDs and teardown, and libinput's open, close and log callbacks with
them. The reader dispatches whenever libinput's fd is readable, however long
the compositor loop is busy, so the kernel's per-client evdev buffer (64
events for a keyboard, about 16 keystrokes) never fills behind a slow frame.
It translates events into batches and hands them to the compositor loop with
`Loop.Hand`, in libinput's order and with its timestamps; device arrivals,
removals and reports travel in the same stream. At most 65536 input events wait
undelivered; beyond that the reader drops events and the loop reports how many.
libinput's `SYN_DROPPED` (the kernel overflowed before the reader got there)
and every message at libinput's error priority are reported too, so no loss is
silent.

The compositor loop reaches the reader only through the reader's mailbox.
Hotplug, suspend, resume and LEDs are posted and return at once;
`Reconfigure`, `Park` and `Unpark` wait for the reader's answer, which comes
between two libinput calls. The reader never waits on the compositor loop.
Devices are taken from and given back to logind on a third goroutine
(`loop.Serial`), so neither loop waits on D-Bus. An inactive session suspends
libinput on the reader, which closes the devices, so nothing is read until the
resumed devices are handed back. Parking for an upgrade stops dispatch on the
reader and delivers everything already read before `Park` returns; nothing is
read again until `Unpark`. `Close` stops taking devices, waits for the takes in
flight, stops the reader, tears libinput down on its thread, and delivers the
removals.

The loop never waits on another process, the GPU or client memory. Off the
loop (`Compositor.offLoop` or a package's own goroutine), result applied on
the loop:

- D-Bus calls once the session is up (devices, VT switches, the lid policy,
  portal settings).
- Config reloads (file, font, keymap); a bind's `exec`, forked in order.
- Connector probes (EDID over DDC), cursor theme files, tab label rasters.
- shm reads that could fault: a pool not on tmpfs or hugetlbfs, or more than
  `1 << 20` damaged pixels on a surface already showing a buffer, is copied by
  a goroutine first, gating the commit like an implicit-sync dmabuf.
- Captures into shm: fence wait and row copy. An abandoned capture sets an
  atomic `cancelled` flag the copy checks per row, the one shared-state
  exception.
- The Xwayland window manager's X connection.

On close the compositor waits for these goroutines.

The loop goroutine is locked to its OS thread (`gpu.PinThread`), so its GL
context stays current between renderer calls; on any other goroutine the
renderer makes its context current around each call.

The pinned thread and the input reader's thread raise their priority the same
way (`cmd/lys-wm/priority.go`), always with `SCHED_FLAG_RESET_ON_FORK`:
`SCHED_RR` 1, else `SCHED_RR` through rtkit, else nice -10, else nice -10
through rtkit. The reader raises its own thread before it opens libinput and
drops the raise before the thread ends, so a loop burning the only CPU cannot
starve it. Packages grant the limit through PAM
(`packaging/limits.d/90-lys-wm.conf`), never a file capability, which would put
libc in secure mode and hide driver environment variables.

A `SCHED_RR` thread under a finite hard `RLIMIT_RTTIME` (always so through
rtkit, which demands one) gets a guard: a thread at `SCHED_RR` 2 waiting on a
CPU-time timer of the watched thread. Every tenth of the hard limit the thread
runs, the guard reads its voluntary context switches; when it ran half a tenth
or more without one, the guard moves it to `SCHED_NORMAL` at its nice value,
logs how long it ran, what it was doing and its goroutine's stack, and after a
backoff (1 s doubling to 1 min) hands the thread a job to take `SCHED_RR` back
itself and block once before the guard trusts it again, since the kernel only
clears the thread's realtime run when it wakes at a realtime policy. The guard
outranks the threads it watches, so no time-sharing load delays it; it does not
rely on `SIGXCPU`, which the kernel answers by raising the soft limit a second
for the whole process. rtkit's soft limit stays at its hard one. A thread whose
guard cannot be raised does not stay realtime.

## Errors that end the session

`Loop.Run` returns the first error a callback returns, and `lys-wm` exits with
it. `backend.Fatal` is the one predicate that decides which errors may reach
it from a scoped path (frames, planes, clients, transactions). It accepts only
errors lys-wm's own code types:

- **Device gone**: `backend.ErrDeviceLost`, wrapped where the condition arises
  on lys-wm's own devices. Every ioctl the DRM backend issues on its card goes
  through `backend/drm`'s `device`, which turns an `ENODEV` into
  `ErrDeviceLost`, naming the card, once the card confirms it by answering
  `ENODEV` to a capability query too. The nested backend's host connection
  hanging up or refusing a write, and a GPU reset too often, are wrapped the
  same way. A raw `ENODEV` says nothing about whose fd it came from: one from
  a client's resource (a dmabuf, a syncobj, an shm pool, a buffer exported by
  another GPU) never ends the session.
- **Broken invariant**: `backend.Broken`, a bug that should crash loudly: a
  frame, damage, change or removal for an output the compositor never placed,
  `BeginFrame` with a frame in progress, `Present` without one, a DRM event read failing other than with `EAGAIN` or `EINTR`
  (`ENODEV` there is the device gone).

Also fatal, returned to the loop directly rather than through a scoped path: a
failed `epoll_wait` or mailbox read in `Loop.Run`, a DRM event read error, a
failed rescan when a device resumes.

Everything else is scoped and reported through `Config.Report`:

| Scope | Failure | Recovery |
| --- | --- | --- |
| Frame | Render, readback, test or commit failure (`EBUSY`, `EINVAL`, GL error, `EACCES` after losing master, an `ENODEV` the card does not confirm) on a live output, a mirror or a stalled output's probe | `AbortFrame`, invalidate the scene, schedule another frame at the frame clock's pace. The first drop and the first recovery are reported. Commits failing for `SustainedFor` stall the output ([The lid](#the-lid)); a stalled mirror stays a mirror and keeps retrying. |
| Output events | A backend's add, change, damage or removal of an output, whichever of live, mirror, stalled or disabled the compositor placed it as (`placement` in `compositor/outputs.go`, one method per event); output rules reapplied after a change, a stall or the lid, failing to commit a mode or power state | Every placement handles every event: damage schedules a live output, a mirror or a probe and does nothing on a disabled one, as the DRM backend's `Wake` and `Resume` damage every output. A refused rule commit is reported and the output stays as it was, still placed by its state; the next output or lid event applies the rules again. A configuration a client or the config file applies gets the error back instead. |
| Planes between frames | Cursor or gamma commit rejected | Reported once, state stays dirty and is committed again. A cursor refused with `EINVAL`/`ERANGE` moves into the frame. |
| Client | A failing request | A request meeting an error `backend.Fatal` accepts stops the compositor, naming the client. Any other error, a client resource's own failure included, is sent to that client as `wl_display.error` and kills only it (`server.Client`, #428); a failed capture sends `failed`. |
| Client teardown | A destroy hook failing as a client leaves, is killed or the compositor closes | Every remaining hook still runs. A protocol error for a client already gone is dropped; an error `backend.Fatal` accepts reaches the loop; anything else is reported once with the client and the object. |

### GPU reset

A lost GLES context (`gpu.ErrContextLost`) or Vulkan device (lys-lib
`gpu.ErrDeviceLost`) is `backend.GPULost`, not fatal. The EGL context is
created with `EGL_LOSE_CONTEXT_ON_RESET`.

- After a loss every renderer call is a successful no-op (`render.LostPass`,
  `LostRead`); no caller sees an error. `Renderer.Lost` is the one place a
  loss is reported, asked after every output frame and on a failed frame.
- `Renderer.Recover` rebuilds in place: textures, targets and shaders keep
  their identity, dmabuf-backed ones reimported from their open fds. The
  compositor then re-uploads shm surfaces, drops what it drew itself and
  repaints every output (`compositor/gpureset.go`).
- A target the Vulkan renderer exports (`NewExportedTarget`,
  `NewLinearCopyTarget`) hands out its dmabuf as a `gpu.Buffer` the caller
  owns and closes. It outlives the target and the device, so nothing a caller
  holds dies in `Recover`; the renderer duplicates descriptors for itself only
  when a device is lost.
- More than lys-lib's `gpu.LossLimit` (3) losses within `LossWindow` (1 min)
  returns `backend.ErrDeviceLost`. Secondary-GPU blitters in `backend/drm`
  recover the same way, bounded separately.
- `Renderer.LoseDevice` raises a loss for tests.

## Pure core

`internal/wm` knows nothing about Wayland, X11, rendering or I/O. It is a
value-level model of the container tree, workspaces and focus: functions from
state and event to new state and effects. The protocol glue translates client
requests and backend input into `wm` events, and `wm` effects into protocol
events and render state.

### Container tree

Each workspace is a tree in the hy3 model. Behaviour is read from hy3; its
code is never ported.

- Groups: a root group (horizontal when the area is wider than tall), split
  groups whose child ratios always sum to the child count, tab groups showing
  one child under a tab bar; windows are leaves.
- A new window goes right after the focused window in the same group and takes
  focus.
- Removing a child shares its ratio among its siblings; focus moves to the
  previous sibling, or the next if it was first.
- Removal collapses only invalid groups, walking up: an empty group goes; a
  group with one child collapses into it when it is an active ephemeral group,
  a split holding a split, or a tab group holding a one-tab tab group. The root
  keeps a lone window. hy3's `node_collapse_policy` (2) applies only to moves.
- An ephemeral group around one window is staged, becomes active at two
  children, and dissolves when it drops back to one.

Layout (`wm.Arranger`) is a pure function from tree, area and spacing to
placements. Split edges are rounded from cumulative ratios, so children cover
the area exactly. Gaps shrink the area by the outer gap, then take the inner
gap around each split edge (odd pixel after the edge), so neighbours are
exactly the inner gap apart at any depth. Directional focus, moves and drops
work on the gapless arrangement.

### Workspaces

Workspaces follow Hyprland:

- Numbered from 1, created on first use, dropped once empty and not shown. A
  new output shows the first free one.
- Switching to a workspace shown on another output focuses that output.
- `e+N`/`e-N` step through open workspaces of every output in ID order,
  wrapping. `previous` is the focused output's last workspace, recreated if
  dropped.
- Outputs are known by connector name: unplugging moves their workspaces to
  the focused output, the first left if the lost one was focused; replugging
  returns them. What was shown stays shown: the lost output's workspace and
  scratchpad, if they hold windows, take over the receiving output when the
  lost one was focused or the receiver shows no window, and keep focus.
  Floating windows move with their workspace. A replugged output shows the
  focused workspace if it is coming home, and focus follows it; the receiver
  goes back to the workspace it showed before. After the last output goes, the
  next one shows what it showed. An output that stops displaying (its commits
  stalled, [The lid](#the-lid)) goes and comes back the same way.
- Workspace rules are resolved to connector names whenever outputs change:
  bound workspaces live on their output, `default` is shown first,
  `persistent` is never dropped, a name is a second address. Selector rules
  (`single_tiled`, `fullscreen`, `any`) set gaps, border, rounding and shadow
  at arrange time; each placement carries its workspace's look.

### Animations

`internal/anim` is pure: bézier easings, closed-form springs, and tweens that
sample a curve at a monotonic time and retarget from the value they show. The
compositor keeps a tween per window over its tile, sets it when a layout change
lands, and samples it once per frame at the vblank the frame aims at, or at
the frame's start without one (`Config.Clock` lets tests drive time). See
[Frame pacing](./frame-pacing).

- A window's draw ops are recorded at its goal geometry and mapped onto the
  animated tile, so surfaces, border and popups move as one and the op diff
  damages exactly what moved.
- An output schedules frames for as long as an animation overlaps it.
- Key-triggered default springs cover at least 7.5% of their travel on the
  first 60 Hz frame, half by the fourth and 95% by the tenth
  (`anim/defaults_test.go`), with no initial velocity.
- A closing window leaves the window list (input, layout) at once; a snapshot
  of its surface tree and border, taken at unmap, animates above its output's
  windows until its animation ends.

### Layout changes land together

A layout change that gives windows a new size is a transaction
(`compositor/transaction.go`). `reconcile` sends configures at once but
installs placements (tile, rect, layer, stack, tab bars, the animations they
start) only when the change lands. Drawing, hit testing and popup placement all
read those placements, so what is shown and what takes input are one state.
Keyboard focus and `activated` do not wait.

- **Who is waited for**: a visible, non-minimized window on an output whose new
  size differs from both what it was last asked for and what it has drawn. No
  such window: the change installs in the same call, no timer, no hook.
- **Mapping is not held**: a new window fades in on its first frame;
  neighbours that have not drawn their new size are stretched.
  `Config.HoldOpening` restores the hold.
- **Barrier**: a commit answering the configure is parked by a
  `surface.Barrier` at the end of its gates, so it counts as ready only once its
  fence, FIFO and commit-timing gates are open. Its frame callback is parked
  too. On xdg-shell the answer is the ack of the last configure with a buffer;
  on X it is a commit at the configured size, plus a raised
  `_NET_WM_SYNC_REQUEST` counter for clients that use it. A null attach unmaps
  at once. Synchronized subsurfaces park with their parent.
- **Landing**: when the last member is ready, or at the bound, placements are
  installed and every barrier released in one loop pass.
- **Bound**: `DefaultHoldBound`, 100 ms, not configurable. A client that misses
  it is marked stalled and not waited for again until it catches up.
- **No queueing**: anything that changes the arrangement during a hold (window
  opens, closes, unmaps, client dies, workspace switch, output goes) lands the
  old change at once and starts over.
- **Mismatched content**: a client that has not answered is scaled to its
  frame (`frame.stretch`), on every renderer. A client that answered with a
  different size is drawn at its own size, clipped to its frame. Hit testing
  uses the client's real geometry.
- **Drags coalesce**: pointer motion only marks a step; the next frame moves
  `wm` state and reconciles, one configure per frame, never while a change is
  in flight. A drag holds for at most `dragHoldBound` (17 ms). Three reused
  `wm.Arranger`s hold the shown, held and next arrangement.

A resized layer surface is placed by its last applied size, held to its
anchors, until it commits for the resizing configure. A surface moving under a
still pointer sends its client a motion unless the pointer is locked.

A client commit wakes only the outputs whose last frame drew its surface tree,
unless its placement may have changed (bounds, role geometry, offsets, gaining
or losing a texture, a new surface, a role without a stated placement such as a
cursor). Anything not known to stay put wakes every output.

## Dispatch

One typed `wm.Action` set and one `wm.Dispatch`. Keybinds,
`lys-wmctl dispatch`, the IPC socket and protocol requests such as
foreign-toplevel activate all call it. Nothing else mutates WM state.

## Input objects

- A tablet tool is in proximity of a surface only through a tool object whose
  tablet seat still holds the tablet: `proximity_in` names the tablet, and no
  motion, tip, button or frame reaches a tool object before it. A client left
  with no such object is not the tool's focus and gets the pointer instead.
- A tool's tip and buttons are held on the seat for as long as the device holds
  them, and the surface the tool enters is told of them with `proximity_in`. A
  tip or button let go while the tool has no focus (its surface destroyed
  mid-stroke, the tool over a client without the tablet protocol) is let go on
  the seat too, so the next surface gets a hovering tool, not a stroke.
- Buttons belong to the seat's pointer, not to one `wl_pointer`: a release
  goes to every `wl_pointer` of the focused client, the one made after the
  press and entered since included, as wlroots sends it. `wl_pointer.enter`
  carries no buttons and the protocol pairs no release with a press, so a
  client that replaces its `wl_pointer` under a held button still learns that
  the button was let go.

## Content updates

- Each `wl_surface.commit` makes a content update. Extensions attach gates it
  must pass before applying (explicit-sync acquire point, fifo barrier,
  commit-timing target) and state that becomes current with it (release point,
  alpha multiplier, constraint region, colour description, content type).
- Updates apply strictly in commit order; a held update holds back later ones,
  so fifo queues frames instead of dropping them.
- A synchronized subsurface keeps one cached update that absorbs its commits
  and enters its queue when the parent applies.
- Unapplied state (pending, sync cache, queued update) locks its buffer. A
  client may destroy such a `wl_buffer`; its memory is released only when no
  state holds it. Applied state does not lock its buffer.
- A frame latches what it draws: presentation feedback moves from the surface
  to the frame and is reported at the next vblank. An update that applies
  after the latch is not reported for that refresh.
- The fifo barrier clears at the latch, at once when no powered output shows
  the surface, and when an output powers off or goes away.
- A commit-timing target is due once the earliest next flip of an output
  showing the surface reaches it, or at the target time, guaranteed by a timer.

## GPU

### Devices

- Nothing assumes one GPU. Each DRM card on the seat gets its own `backend/drm`
  instance. Every output is rendered on one primary GPU, since client buffers
  become textures in its context. The primary is passed explicitly, never a
  global.
- Primary (`choosePrimary`, `cmd/lys-wm/native.go`): `gpu` in the config, else
  the boot VGA device with a render node, else the first card with a render
  node, else the first card rendering in software. Choice and reason are
  logged.
- Secondary cards show frames the primary rendered. With a render node: GPU
  blit from a linear buffer the primary renders (or, on NVIDIA, a linear copy
  made by its Vulkan copy engine), ordered by sync files. Without one (vkms,
  DisplayLink): CPU copy into linear dumb buffers. A Vulkan session blits with
  Vulkan, falling back to GLES on a card without a Vulkan driver.
- Cards come and go with udev. A removed secondary drops its outputs; losing
  the primary is fatal. Connector names are globally unique across cards.
- GBM and EGL get their own file on the device (the render node, or a second
  card file where the stack needs dumb buffers), and the Vulkan driver opens
  its own, because GEM handles are per file. KMS imports close their handles
  once the framebuffer holds a reference.

### Renderers

| Renderer | Selected by | Notes |
| --- | --- | --- |
| GLES2 | default for nested; `-renderer gles` on drm | glvnd picks the vendor; GBM, device or surfaceless platform; one configless context per display, no surfaces. The drm path for a card without a Vulkan driver |
| Vulkan | default for drm; `-renderer vulkan` elsewhere | Peer of GLES in every path. On drm it needs a Vulkan driver for the primary card: without one lys-wm stops at startup with `drm.ErrNoVulkan`, naming the card, `-renderer gles` and `LYS_WM_RENDERER=gles`, and never falls back to GLES by itself. lavapipe drives no DRM node, so vkms and virtio-gpu without venus run `-renderer gles` on llvmpipe, as the NixOS VM checks do (`nix/vm.nix`). On drm it takes its scanout buffers from GBM with `GBM_BO_USE_SCANOUT` and imports them to render, since xe refuses ADDFB2 for memory Mesa's Vulkan driver allocates (it binds it into its VM before the scanout flag can be set); it loads no EGL or GLES unless a secondary card has no Vulkan driver (`TestStartOnEveryCardWithoutMaster`, with those two libraries unloadable). Nested: renders into memory it exports, on the device the host's dmabuf feedback names. Headless: tries render nodes first |
| Software | default for headless | Reference for every pixel test |

Scanout memory is GBM's under both renderers (`gpuAllocator` over
`gbmMemory`), in a modifier from the primary plane's `IN_FORMATS` the renderer
also draws into. An allocator is vouched for only once its probe buffer has
become a framebuffer on the card, so it is always opened on a card node, never
a render node. The
allocator picks among those; when the modeset's test commit refuses its pick,
the next try drops that modifier, so the list falls down in the driver's order
before the next format is tried, and the winner is reported. Every
renderer is pixel-compared against the software renderer by
`render/rendertest`.

A client dmabuf that names no modifier is sampled under GLES the way the
display samples the linear one: through `GL_TEXTURE_EXTERNAL_OES` where
`eglQueryDmaBufModifiersEXT` lists linear as external-only, as NVIDIA does
(`TestAnEGLHostImportsThoseBytesWithNoModifier`). A dmabuf query that returns
`EGL_FALSE` and leaves `EGL_SUCCESS` is outside
`EGL_EXT_image_dma_buf_import_modifiers`, which names an error for every
failure and returns `EGL_TRUE` with a count of zero for a format with no
modifiers; it is reported as `gpu.ErrUnsupported`, naming the vendor and the
query, and the compositor then offers no `zwp_linux_dmabuf_v1`. NVIDIA's EGL
does this, and fails `eglCreateImageKHR` the same way, in a process that
brought up NVIDIA's Vulkan driver before it first initialised EGL; a process
that initialised EGL first imports on every later display. The nested
package's test therefore opens its EGL host before its Vulkan device, and
`just docker hardware` selects no Vulkan test of that package ahead of it.

### Scanout and planes

- **Direct scanout** (`compositor/scanout.go`): when the culled frame below the
  cursor is one untransformed, full-alpha client surface covering the output,
  or one inside the letterbox fill, and its colour space passes through to the
  output, its dmabuf goes straight to the primary plane if a test commit takes
  it. Its release waits until a later flip takes it off screen. Viewport
  rectangles map to `SRC_*`/`CRTC_*`; refusals are cached per buffer and
  rectangles.
- **Overlay planes**: opaque, untransformed, unrounded windows of at least
  256x256 go to overlays, up to `backend.MaxOverlayPlanes` (3) per CRTC,
  topmost first, with nothing painted above them. At most one test commit per
  frame; results are cached.
- **Scanout feedback**: a surface that scans out, or is an overlay candidate
  for two frames running, gets a dmabuf feedback tranche of the plane's formats
  and modifiers.
- **Tearing**: with `tearing` on, a fullscreen direct scanout of a surface that
  asks, a rule allows, or is a game without a hint flips async (`FB_ID` only).
- **Bit depth**: composited frames are XRGB8888 unless an output rule sets
  `bitdepth: 10`, which tries XRGB2101010 then XBGR2101010 with the plane's
  modifiers, falling back to 8 bits only when every format's test modeset is
  refused.
- **Inheriting a splash**: an output keeps the CRTC and primary plane already
  lit in its chosen mode. Nothing commits to it until a client surface has
  content on it, or `Config.InheritTimeout` (30 s) passes; that first frame
  swaps only the framebuffer, without `ALLOW_MODESET`.

### Cursor

The pointer rides a cursor plane where there is one: a linear ARGB8888 dumb
buffer, the one buffer every cursor plane takes. Theme and 1:1 shm cursors are
copied from CPU pixels; scaled or transformed ones are rendered and read back
asynchronously, or stay in the frame on a renderer without async reads. A
cursor commit sets only what changed since the last accepted commit and goes
out at the plane lead before the vblank. An image larger than the plane, or a
refused one, puts the cursor into the frame for the output's lifetime.

### Damage

A frame's damage is the difference between this frame's op list and the last.
Lists are aligned (common prefix and suffix, then a bounded look-ahead); only
unmatched ops damage. Surface damage is clipped by opaque ops above it. Above
`maxDamageRects` (32) rectangles, the pair costing the least area merges.

### Masks and effects

- Every draw takes a coverage mask: a rounded rectangle to keep and one to cut,
  antialiased by signed distance. lys-lib's `gpu` owns the formula once
  (`gpu.MaskGLSL` for GLES and Vulkan, `gpu.Mask.Coverage` for software).
- A masked op covers only its solid part for culling; direct scanout never
  takes a rounded window.
- Effects render into a `Renderer.NewTextureTarget` and sample it in a later
  pass. Passes run one at a time; a pass cannot draw its own target.
- **Blur** (`compositor/blur.go`): every blur, from `decoration.blur` or an
  `ext_background_effect_v1` region, is one `drawBlur` sampling a dual-Kawase
  chain kept per surface in the output's scene and freed when unused. Tiled
  windows (unless `live_tiled`) and windows over nothing but the background
  share the output's background chain. `resolveBlurs` draws, after culling and
  in stacking order, only chains a surviving op needs.
- **Look** (`wm.Look`, `compositor/glass.go`): tint is a masked fill, grain
  lys-lib's `render.GrainTile`; colour adjustment and refraction use
  `Pass.DrawGlass` (`GlassGLSL` on GPUs, `render.RefractPlan` in software).
  Mica is the look over a shared chain of the layers below the windows.
- **Overview**: windows draw live; workspace thumbnails are cached targets,
  redrawn at most one per output per frame after a commit damages them.
- **Screen shaders**: the output's ops replay into a retained image drawn
  through the shader. `internal/shader` type-checks a GLSL ES 1.00 subset and
  emits GLSL, SPIR-V (validated by `spirv-val` in tests) and a Go program.
  Captures and a locked session bypass it. See [Screen shaders](./shaders).

## HDR

HDR is per output: the `hdr` rule is `false` (default), `true` or `fullscreen`
(signal HDR only while one HDR-tagged surface fills the output, with
`DefaultHDRHold`, 1 s, before switching back). Off, no pass calls `SetColor`,
no shader variant compiles and no target is float.

- Colour maths is `colorspace`: pure functions from a `Description` and a
  `Target` to an `lgpu.ColorTransform`, evaluated by `gpu.ColorGLSL` on GPUs
  and its Go mirror in software, held together by `rendertest.ColorParity`.
- An HDR frame draws twice: the scene into a per-output half-float working
  target (linear BT.2020, 1 at SDR white, damage only), then one PQ encode into
  the 10-bit swapchain. Blur, shadows and snapshots there run in linear light.
- SDR content decodes as gamma 2.2 on sRGB primaries, tagged content by its
  description. Highlights above the panel compress above 3/4 of the peak on the
  largest channel; out-of-gamut colours clip. Captures and SDR outputs get the
  same transforms to gamma 2.2; a working target's texture is marked `Working`
  so captures convert it.
- `colorApply` turns non-finite values into zero, since a NaN would blend
  through every later draw into a partially repainted target.
- KMS: the modeset is tested with `Colorspace` BT2020_RGB, `max bpc` 10 and
  `HDR_OUTPUT_METADATA` from the EDID; a refusal leaves the output SDR.
- Gamma LUT and CTM act after the encode: on an HDR output gamma clients get
  `failed` (`GammaBlocked`) and the CTM folds into the encode. The cursor plane
  stays in use with its image encoded on the CPU to the output's signal
  (`compositor/hdrcursor.go`).
- Tested against fake KMS, lavapipe and llvmpipe only.

## Tiled displays

Connectors with a `TILE` property form a group. When every tile is connected
and no member already lists a whole-picture mode, the group is one output,
named after the top-left connector, with one framebuffer of the whole size.
Every commit sets all tiles' CRTCs and planes together, and the frame is
presented when every CRTC's flip event arrives. Gamma, CTM, HDR, content type
and adaptive sync go to every tile. A tiled output has no cursor or overlay
plane and never flips async. An incomplete group lights each connector as its
own output.

## Variable refresh

An output rule's `vrr` is `off` (default), `on` or `fullscreen`
(`compositor/vrr.go`). `fullscreen` turns it on while one surface alone fills
the output and its content type, from `wp_content_type_v1` or a rule's
`content`, is game or video. A visible window whose rule sets `vrr` turns it on
too. The compositor sets it through `backend.AdaptiveSync` each frame; the DRM
backend writes `VRR_ENABLED` only on change or modeset, never in an async
flip. Capability needs a `vrr_capable` connector on a CRTC with `VRR_ENABLED`.

While on, nothing assumes a fixed interval: frames start when asked, planes
commit at once, animations sample the clock. `wp_presentation` v1 reports
refresh 0; v2 reports the mode's interval. A cursor move commits at once,
except while one surface fills the output (`backend.FrameFilling`): then the
cursor rides the next frame and commits alone only after the panel's longest
frame (`edid.ParseVRR` minimum refresh, else 24 Hz).

## Power saver

While power-profiles-daemon reports `power-saver`, every output above
`power_saver_frame_rate` presents on every k-th vblank, k the refresh over the
cap rounded up (`backend.Mode.PacedInterval`); under VRR the cap is a minimum
frame interval. A frame's turn counts from the frame last shown, never from
the latest vblank: it aims at the first vblank at least the paced interval after
that flip (`backend.Pacing.Aim`), and its latch starts no earlier than the
refresh before. The vblank anchor and the last flip are separate facts, so
re-anchoring on the CRTC's vblank never moves a turn. Presentation feedback
reports the paced interval; the cursor plane keeps the mode's own. `internal/powerprofile` follows
`ActiveProfile` (`org.freedesktop.UPower.PowerProfiles`, then
`net.hadess.PowerProfiles`) off the loop; no daemon means no cap.

## The lid

lys-wm holds logind's `handle-lid-switch` inhibitor in block mode and applies
one policy on every relevant change (lid, outputs, power supplies): suspend
when the lid is closed, the laptop is not docked and the machine is on
battery (`compositor/lid.go`). `lid_suspend: false` hands the lid back to
logind.

Docked is one fact, read by both the clamshell rule (`eDP-*` panels off while
the lid is closed) and the suspend: an external output in the layout that
displays (`backend.Displays`: enabled, and its commits take). A connected
output is not enough: after an undock a connector can stay listed while every
commit to it fails with `EBUSY` (#436).

- A backend output tracks its commits in `backend.Commits`. Every failed
  commit (frame, modeset, cursor or colour planes) starts or extends a failing
  streak; one that takes ends it. A failing streak that has lasted
  `SustainedFor` (1 s) at a failed commit stalls the output: one such commit
  is about 60 frame retries, far past a transient `EBUSY`, and the panel is
  dark for at most a second. A lost device never stalls; the session ends
  instead.
- Recovery is the mirror image: a stalled output displays again only after a
  streak of commits that all took has lasted `SustainedFor`, judged at a
  commit that takes. A failed commit ends that streak, so an output that fails
  for a second, takes one commit and fails again stays stalled instead of
  flipping the panel about once a second.
- The stall and the recovery are reported once each, and reach the compositor
  as `OutputChanged` from an idle callback, never inside a frame; the
  compositor re-applies the output rules and the lid policy.
- For placement a stalled output is a lost one. `compositor.place` is the one
  decision for where an enabled backend output goes: an output that does not
  display leaves the layout through the same `disable` an unplugged output
  takes, so `wm.RemoveOutput` moves its workspaces with the takeover rule of
  [Workspaces](#workspaces), its layer surfaces close and its `wl_output`
  global is withdrawn: clients see what they see on an unplug, and nothing is
  drawn for a screen nobody sees. A "not displaying" state inside the wm was
  rejected: every path that walks the layout (areas, layers, input, capture)
  would need a second case for an output that is listed but empty.
- A stalled output stays enabled and held apart (`Compositor.stalled`, a head
  that is off in wlr-output-management). It is drawn a letterboxed probe frame
  at its frame rate, which is what gives `backend.Commits` the commits it
  judges; a failed probe is that verdict's input, not a dropped frame. Once it
  displays it comes back through `enable`, as a replugged output does: its
  workspaces go home, and the panel's rule, applied after the externals, turns
  the panel off again under a closed lid.
- On a stall the drm backend probes its connectors again, as on a hotplug
  uevent, so a connector that left without one is removed. Removing an output
  whose disabling commit fails is reported, not fatal.
- The output rules apply to external outputs first and panels last, so the
  panel's rule reads where the externals ended up.

- The power read and Suspend run off the loop, one check at a time.
- A sleep is pending from Suspend accepted (or `PrepareForSleep(true)`) until
  `PrepareForSleep(false)`; the policy does not ask again meanwhile.
- A refused Suspend is reported, never fatal. A power supply that disappears
  while read is skipped; any other read error leaves the state unknown and does
  not suspend.
- After a wake the policy runs again once `lidHoldoff` (30 s) passes.
- The policy also runs once at startup, after every card's connectors are
  known, so a session started lid-closed, undocked and on battery suspends
  (#431).

## Xwayland

`internal/xwayland` starts Xwayland lazily:

- At startup lys-wm takes an X display with a lock file in `/tmp`, binds
  `/tmp/.X11-unix/X<n>` and the abstract socket, and exports `DISPLAY`.
- The first connection launches Xwayland `-rootless` with both sockets as
  `-listenfd`, `-displayfd` for readiness, a socketpair as `-wm`, and another
  as `WAYLAND_SOCKET`. After a crash the sockets stay with lys-wm and the next
  connection restarts it.
- The window manager is an X client (`jezek/xgb`) on the `-wm` socket. X
  requests block while Xwayland makes Wayland roundtrips to lys-wm, so it runs
  on its own goroutine and reaches state only through the loop.
- `xwayland_shell_v1` is visible only to Xwayland and maps each window's
  `WL_SURFACE_SERIAL` to its `wl_surface`.

Zero scaling: Xwayland sees each output in physical pixels at scale 1, placed
at its layout position times the largest output scale, so outputs never
overlap in X space. An X window is drawn and hit at the scale of the output
nearest its corner, so its buffer lands pixel for pixel; floating X windows are
limited by `WM_NORMAL_HINTS` converted to logical pixels.

Bridges, both ways:

- `CLIPBOARD` and `PRIMARY` (`text/plain;charset=utf-8` is `UTF8_STRING`,
  `text/plain` is `TEXT`), INCR in 64 KiB chunks, 30 s transfer timeout.
- Drag and drop through Xdnd. An X drag reaches Wayland windows through an
  input-only `XdndAware` proxy window mapped over the root while the surface
  under the pointer is not an X one.

## IPC

`lys-wmctl`, bars and scripts use a Unix socket at
`$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock`, exported as `LYS_WM_SOCKET`.
One JSON request line, one JSON response line carrying the protocol version; a
`watch` request turns the connection into an event stream.

- One goroutine per connection posts to the loop and waits.
- Events are not raised where state changes. Before the loop waits
  (`BeforeWait`), the server snapshots outputs, workspaces, windows, submap and
  layout and diffs against the previous snapshot. Only dispatches are reported
  where they happen. No watcher, no snapshot.
- A watcher `watchBacklog` (256) events behind is disconnected.

[IPC](./ipc) has the protocol.

## Portals

lys-wm ships no portal backend. It sets `XDG_CURRENT_DESKTOP=lys-wm` and
installs `share/xdg-desktop-portal/lys-wm-portals.conf`
(`packaging/xdg-desktop-portal/`):

| Interface | Backends, in order |
| --- | --- |
| ScreenCast, Screenshot | lys-shell, then xdg-desktop-portal-wlr |
| InputCapture | lys-shell's `lys-shell-lys-wm.portal`, over the `capture` IPC ([IPC](./ipc#input-capture)) |
| everything else | lys-shell, then xdg-desktop-portal-gtk |

lys-wm reads `org.freedesktop.appearance accent-color` back from the portal
(`cmd/lys-wm/accent.go`); a change posts a config reload.

## Security and private protocols

`wp_security_context_manager_v1` lets sandbox engines register a socket.
Clients on it do not see the globals listed in `privileged`
(`compositor/sandbox.go`): capture, data control, foreign toplevels, output
management and power, virtual input, session lock, layer shell, workspaces,
global shortcuts, hotkeys, shortcut inhibit, gamma, CTM, input methods and
every `lys_*` protocol.

Private protocols are pinned from lys-lib:

- **`lys_escape_v1`**: a one-shot claim on the next unmodified Escape. A key
  goes to the first that wants it: compositor bind, lock surface, popup grab,
  exclusive layer, input-method grab or drag, a shortcut-inhibiting focus, the
  armed client when focused, the newest arm, the focused surface. A key the
  window saw go down is never taken.
- **`lys_virtual_touch_v1`**: a touchscreen spanning the layout's bounding
  box for remote desktop, with its own block of touch ids.
- **`lys_capture_hint_v1`**: declares a capture a one-shot screenshot; every
  other capture is a screencast. The `block_out_from` rule keeps a surface out
  of screencasts by replaying the frame with its ops replaced by opaque fills
  of the same shape.

`xx_hotkey_v1` is offered only with `client_hotkeys: true`; hotkeys match
after compositor binds and before anything else, and triggers are exclusive.
`hyprland_focus_grab_v1` acts as an xdg popup grab over a whitelist of
surfaces.

## Protocol bindings

- `internal/protocol/protocols.json` pins every protocol source and maps each
  XML file to a package. `go run ./internal/scanner -fetch` refreshes the
  vendored XML; `go generate ./internal/scanner` regenerates both sides; a test
  fails when the committed tree is stale.
- The XML is not shared with lys-lib's `wlr`: the server advertises the newest
  version at a pinned source, while `wlr` is the client API lys-ui and
  lys-shell build against.
- Each interface becomes a type wrapping `wire.Object`. Incoming messages go to
  a per-interface `Handler` with one typed method each; object arguments are
  looked up and type-checked first. A message that creates an object returns
  that object's handler, so no object exists without one.
- Sending a message newer than the bound version is an error, never a silent
  drop. The server's client and the client-side display implement `wire.Peer`.

## Upgrading a running session

`lys-wmctl upgrade [--drop-x11] <path>` moves a session started with
`--holder` to another lys-wm binary, keeping its clients. Without `--holder`
none of this runs. [Upgrade](./guide/upgrade) is the user guide.

- **Holder and worker**: with `--holder`, the process the display manager
  starts holds the logind session and its devices, forwards signals and exits
  with its worker's status. The compositor is its child and asks it for
  devices (`session/remote.go`). logind ties devices to one D-Bus connection,
  so only the worker can be replaced. The holder protocol is versioned.
- **State is Wayland requests**, never Go structs: the old compositor writes,
  per client, the requests that rebuild its objects; the new one runs them
  through its ordinary handlers while nothing reaches the client.
  `server/journal.go` keeps them by `server/policy.go`;
  `server/testdata/handover-policy.txt` classifies every request of every
  protocol and a test fails on an unclassified one. Surfaces and buffers are
  written from their state (`surface/handover.go`).
- **The seat moves with the session**: the requests replay before the new
  compositor's input devices appear, since those arrive on the loop after
  it starts. The handover state therefore carries the capabilities the seat
  advertises and those it ever had (`seat/handover.go`). `get_keyboard`,
  `get_pointer` and `get_touch` replay against the history that admitted them,
  and the new seat goes on advertising an inherited capability until a device
  of its own backs it, so no client is told of a loss that did not happen. A
  device unplugged during the handover stays advertised until one of its kind
  has come and gone.
- What cannot be rebuilt is retired with an event any compositor may send
  (popup dismissed, head closed, source cancelled, capture failed). What cannot
  be retired refuses the upgrade up front, naming client and reason: a session
  lock, a sandbox listener, an exported window, Xwayland unless `--drop-x11`.
- **Commit point**: the old compositor stays parked until the new one has
  rebuilt every client and presented on every lit output; any earlier failure
  resumes the old one unchanged. Both share the holder's DRM file. Client
  connections and listening sockets pass as fds (`handover`), so
  `WAYLAND_DISPLAY` and `DISPLAY` stay valid.

## Testing

- Unit tests for the pure core and every package with logic of its own.
- Golden images, byte-exact, from the software renderer (`golden`).
- `render/rendertest`: every GPU renderer is pixel-compared against the
  software renderer, including `ColorParity` for colour transforms.
- `harness` boots the same compositor `cmd/lys-wm` runs on the headless
  backend, in process, with a private `XDG_RUNTIME_DIR`, and drives real
  clients from the dev shell. `just test` sets `LYS_WM_HEADLESS=1`, turning a
  missing tool into a failure (`testenv`). `just gates`, which CI runs, runs
  the suite under the race detector with the same setting.
- Tests that open the machine's DRM cards or render on its GPU carry
  `OnHardware`, `WithoutMaster` or `OnARenderNode` in their names, and those
  that take DRM master `OnASeat`. `just test` and `just race` skip all four
  (`host_skip` in the justfile), so a run beside a desktop session never opens
  a card; `just hardware` refuses to run outside the container `just docker
  hardware` builds, and `vm-seat` runs the seat tests.
- Commands the compositor execs write to `Config.ExecOutput`, which
  `PlainConfig` sets to the process's stdout and stderr. A test that reads an
  exec's output names a pipe there: the harness tests share one process and
  run in parallel, so none replaces `os.Stderr` or another process global.
- The dev shell points EGL at llvmpipe (`__EGL_VENDOR_LIBRARY_DIRS`,
  `LIBGL_ALWAYS_SOFTWARE=1`) and `VK_DRIVER_FILES` at lavapipe. Unset them to
  test on the machine's driver.
- dmabuf paths need `/dev/udmabuf` on lavapipe, so they run in the
  `vm-gpu-tests` NixOS check; `just docker hardware` runs them on a real GPU
  with both renderers. `vm-xdnd` drags files between Wayland and X both ways.
- The DRM backend's tests run a fake device on a loop from `loop.NewManual`,
  whose clock stands still unless a test sets it: with nothing ready and no
  work off the loop on its way back, the rig moves it to the next armed timer,
  and a wait that nothing left can end fails at once. No such test reads the
  wall clock, so latches, flips and missed vblanks land the same on a loaded
  machine; a timer armed for no time at all is due at once, as one always is
  by the time a real loop looks.
- Frame pacing is gated by `lys-wm-pace` ([Frame pacing](./frame-pacing)).

## Reference implementations

Hyprland's backend library [aquamarine](https://github.com/hyprwm/aquamarine),
its binding generator
[hyprwayland-scanner](https://github.com/hyprwm/hyprwayland-scanner) and its
compositor-in-the-loop suite `hyprtester` are the references. Issues link the
relevant files at pinned commits.

Licences decide how they may be used:

- Hyprland, aquamarine and hyprwayland-scanner are BSD-3-Clause; libwayland is
  MIT. Their logic may be ported; ported code keeps the original copyright
  notice and licence in `THIRD_PARTY.md`.
- hy3 is GPL-3.0. Read it to pin down behaviour; never port its code.
- KDE's `server-decoration` XML is LGPL-2.1-or-later, vendored unchanged with
  its licence header; no KDE code is ported.
