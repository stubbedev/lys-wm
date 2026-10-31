# Architecture

The decisions every change builds on. Scope and non-goals are in the
[README](../README.md); the plan is in the issues. When a decision changes,
change it here in the same commit and say why in the issue that changed it.

## Pure Go

lys-wm builds with `CGO_ENABLED=0`, always. A C library is used only when no
reasonable Go alternative exists, and then it is loaded at runtime with
[purego](https://github.com/ebitengine/purego) (`dlopen`), never linked.

| Need | Source |
| --- | --- |
| EGL, GLES2, GBM | `libEGL`, `libGLESv2`, `libgbm` through purego (#33) |
| Input devices | `libinput` through purego (#42) |
| Keymaps and keyboard state | `libxkbcommon` through purego (#13) |
| Wayland wire protocol | in-repo codec and scanner (#3, #4) |
| DRM/KMS ioctls, pixel formats, EDID | lys-lib's `drm`, `fourcc` and `edid`, shared with lys-splash (stubbedev/lys-lib#30) |
| epoll, timerfd, signalfd, memfd | [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix) |
| logind, systemd | lys-lib's `third_party/godbus`, godbus v5.2.4 with the fd and ordered-dispatch fixes every lys process shares |
| udev | netlink, read directly (#39) |
| X11 window manager | [`github.com/jezek/xgb`](https://github.com/jezek/xgb) (#45) |
| Text | [`github.com/go-text/typesetting`](https://github.com/go-text/typesetting), through lys-lib's `render` |
| Config file | [`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml), strict, as lys-shell uses (#30) |

Adding a purego library, or a dependency to this table, is a decision issue.

libxkbcommon replaced xkb-go (#13): xkb-go's RMLVO compiler drops the
compatibility section, so no key sets a modifier, and serializes keys
with several groups in a form libxkbcommon refuses to parse. Clients read
keymaps with libxkbcommon anyway, so the compositor compiles and serves
them with it too. The purego libraries' paths are baked in at build time
by the Nix package; `LYS_WM_LIBXKBCOMMON` overrides libxkbcommon's.

Tab titles are drawn with lys-lib's `render` package, which lys-ui moved
there with everything else that is not UI (stubbedev/lys-ui#357). Font
discovery is internal to lys-ui, so the tab font is baked in the same way:
the Nix package sets DejaVu Sans as the default and `LYS_WM_FONT` overrides
it, until the config (#30) names one.

## Threading

One goroutine owns all compositor state and runs the event loop (#5). Other
goroutines exist only at the edges, for blocking work such as D-Bus. They
post closures to the loop's mailbox and never read or write state
themselves. Nothing in the compositor takes a lock to protect its state.

Screen capture into shared memory is one such edge (#131). The loop draws
the capture, starts an asynchronous read into a pixel buffer and maps the
client's buffer privately; a goroutine waits on the GPU fence, the loop maps
the pixel buffer, and a second goroutine copies the rows into the private
mapping. Each step hands back to the loop through the mailbox, so the loop
never waits on the GPU or copies a frame. On close, the compositor waits for
these goroutines and releases what their posted closures would have.

The loop goroutine is locked to its OS thread for as long as it runs
(`gpu.PinThread`, #149), so a GL context it makes current stays current
between renderer calls: `eglMakeCurrent` runs only when another display's
context is current on that thread. On any other goroutine the renderer makes
its context current and releases it around every call, as before, since the
goroutine may move to another thread in between. Unpinning releases the
context, so whatever closes the renderer afterwards can make it current.

## Errors that end the session

`Loop.Run` returns the first error a callback returns, and `lys-wm` exits
with it. So only two classes of error may reach the loop (#144):

- **Device lost**: the device is gone and nothing on it can recover.
  `ENODEV` from any ioctl, `EGL_CONTEXT_LOST` or `GL_CONTEXT_LOST`
  (`gpu.ErrContextLost`), and `backend.ErrDeviceLost`, which the nested
  backend wraps around a failed send to its host.
- **Broken invariant**: a bug, which should crash loudly rather than limp
  on. `backend.Broken` marks these: a frame on an output the compositor
  does not know, `BeginFrame` with a frame already in progress, `Present`
  without one.

`backend.Fatal` decides the class. Everything else is scoped to one frame,
one output or one client, and is reported through `Config.Report` and
recovered at the callback boundary:

- **A frame** (`Compositor.Frame`): a render, readback, test or commit
  failure (`EBUSY`, `EINVAL`, a GL error, `EACCES` from a commit that raced
  a lost DRM master) drops the frame. The backend's frame is abandoned
  (`AbortFrame`), the scene is invalidated so the next frame repaints the
  whole output, and another frame is scheduled; the backend's frame clock
  paces the retry to one per refresh. The first dropped frame is reported,
  and so is the first frame that draws after a run of them, so a failure
  that persists does not flood the log.
- **Planes between frames** (DRM page-flip, pacing and frame callbacks): a
  cursor or gamma commit the kernel rejects is reported once and the state
  stays dirty, so the next callback or cursor move commits it again. A
  cursor its test commit refuses falls back to the frame (#143).
- **A client**: a failing request kills that client (`server.Client`),
  and a failed capture sends `failed`, as before.

The rest of the loop's errors stay fatal: a failed `timerfd` or `epoll`
call, a read from the DRM device other than `EAGAIN`, and a failed modeset
on resume, because each means the process can no longer drive its outputs.

## Pure core

`internal/wm` (#21) knows nothing about Wayland, X11 or rendering. It is a
value-level model of the container tree, workspaces and focus, with
functions from state and event to new state and effects. It imports only
the standard library. The protocol glue translates both ways: client
requests and backend input into `wm` events, and `wm` effects into protocol
events and render state.

### Container tree

Each workspace is a tree in the hy3 model (#21): a root group (split
horizontally when the area is wider than tall), split groups that divide
their area by child ratios that always sum to the child count, tab groups
that show one child below a tab bar, and windows as leaves. These rules
were read from hy3 at the pinned commit; its code is not ported.

- A new window goes right after the focused window, in the same group, and
  takes focus.
- Removing a child shares its ratio among its siblings. Focus moves to the
  previous sibling, or to the next one when the removed child came first.
- Removing a window collapses only invalid groups, walking up from where
  the window was: an emptied group goes away; a group left with one child
  collapses into that child when it is an active ephemeral group, a split
  holding a split, or a tab group holding a tab group of one tab. The root
  group keeps a lone window. hy3's configurable `node_collapse_policy`
  (2, empty splits, as configured today) applies only to moves (#23).
- An ephemeral group made around one window is staged, becomes active once
  it holds two children, and then dissolves when it drops back to one.

Layout is a pure function from tree, area and spacing to placements. Split
edges are rounded from cumulative ratios, so without gaps neighbours share an
edge and the children always cover the area exactly. Gaps (#224) shrink the
area by the outer gap and then take the inner gap out around each split
edge, half on each side with the odd pixel after the edge, so every pair of
neighbours is exactly the inner gap apart at any depth. Directional focus,
moves and drops work on the gapless arrangement, which is the topology.

### Workspaces

Workspaces follow Hyprland (#24). They are numbered from 1, created when
first used, and dropped once they are empty and no longer shown; a new
output shows the first free one. Switching to a workspace shown on another
output moves focus to that output. `e+N` and `e-N` step through the open
workspaces of every output in ID order, wrapping around. `previous` is the
workspace the focused output showed before its current one, created again
if it was dropped, so repeating it toggles between the two. Outputs are
known by name: unplugging one moves its workspaces to the focused output,
and plugging it back returns them and shows the one it was showing.

### Animations

`internal/anim` (#223) is pure like `wm`: bézier easings, springs solved in
closed form, tweens that sample a curve at a monotonic time and retarget from
the value they show, and the event table the config resolves through parent
events. The compositor keeps a tween per window over its tile, sets it in
`reconcile`, and samples it once per frame at the frame's start time
(`Config.Clock`, so tests drive time themselves). A window's draw ops are
recorded at its goal geometry and then mapped onto the tile the animation
shows, so its surfaces, border and popups move as one and the op diff damages
exactly what moved. After drawing, an output schedules its next frame for as
long as an animation overlaps it, whether or not the frame changed anything,
so an animation slower than a pixel per frame still reaches its end.

## Dispatch

One typed `Action` set and one dispatcher, in `wm` (#66). Keybinds,
`lys-wmctl dispatch`, the IPC socket and protocol requests such as
foreign-toplevel activate are all callers of it. Nothing else mutates WM
state.

## GPU

`internal/gpu` (#33) loads glvnd's `libEGL.so.1` and `libGLESv2.so.2` and
Mesa's `libgbm.so.1` through purego. The Nix build bakes their store paths
in, and `LYS_WM_LIBEGL`, `LYS_WM_LIBGLESV2` and `LYS_WM_LIBGBM` override them.
glvnd picks the vendor driver, NVIDIA or Mesa, from
`/run/opengl-driver/share/glvnd/egl_vendor.d`. Displays come from the GBM
platform for KMS, the device platform, or Mesa's surfaceless platform; each
has one configless GLES2 context and no surfaces, and renders into dmabufs
or textures.

`render/vulkan` (#155) is a second GPU renderer on lys-lib's `gpu` package,
the Vulkan stack lys-ui and lys-shell draw with; `-renderer vulkan` selects
it on the DRM and headless backends, where it allocates scanout targets in a
modifier the plane and the device share and fences them with sync files.
GLES stays the default. lys-lib loads `libvulkan.so.1`, whose store path the
Nix build bakes in and `LYS_LIBVULKAN` overrides; the dev shell points
`VK_DRIVER_FILES` at lavapipe, as it points EGL at llvmpipe, and both renderers
are pixel-compared against the software renderer by `render/rendertest`.

Without cgo, `os.Setenv` does not reach libc's `getenv`, so variables that
drivers read must be in the process environment from the start. The dev
shell sets `__EGL_VENDOR_LIBRARY_DIRS` to Mesa's vendor and
`LIBGL_ALWAYS_SOFTWARE=1`, so GPU tests render on llvmpipe the same way
locally and in CI. Run them with both unset to exercise the machine's
driver: the dmabuf test only runs where a vendor drives a render node.

The pointer rides a cursor plane where the output has one (#83). The
compositor renders the cursor image, a theme cursor or a client's cursor
surface, into an offscreen target, and the drm backend copies it into a
linear ARGB8888 dumb buffer, the only buffer every cursor plane takes:
NVIDIA's cursor planes scan out linear only, and its EGL renders to no
linear modifier. Pointer motion then moves the plane in an atomic commit of
its own, or in the next frame's commit, and repaints nothing. An image
larger than the plane, or a plane whose test commit refuses it, puts the
cursor back into the frame; a refusal is logged and lasts for the output's
lifetime.

lys-wm drives every DRM card on its seat, one KMS backend each (#62), and
renders every output on one primary GPU, because client buffers become
textures in that GPU's context. The primary is the card named by `gpu` in
the config, else the boot VGA device (`boot_vga` in sysfs) if it has a
render node, else the first card with a render node, else the first card,
rendering in software; the choice and its reason are logged at startup.
The other cards show outputs the primary rendered (#63): their frames are
drawn into the primary's offscreen targets and copied into the card's
linear dumb buffers before each flip, the same copy the cursor plane uses,
so display-only devices such as vkms and DisplayLink work too. A secondary
with a render node gets a GPU blit instead: the primary renders into a
linear buffer the secondary samples and draws into its own scanout buffer.
NVIDIA's GLES renders into no linear buffer, so an NVIDIA primary renders
its usual tiled frame and its Vulkan device (lys-lib's `gpu`) copies it with
the copy engine into a linear buffer its GBM allocates, which the secondary
samples (#147); sync files order the three steps. Outputs keep
the kernel's connector names, which are unique across cards because the
kernel numbers the connectors of each type globally. Cards come and go with udev: a
new card is driven as a secondary, a removed secondary drops its outputs
without touching the gone device, and losing the primary is fatal.

A frame that is only the background and one opaque, untransformed client
dmabuf covering the output skips composition (#101): the buffer goes to the
primary plane directly when the cursor has its own plane and a test commit
takes it. The client's wl_buffer release, and its explicit-sync release
point, wait until a later flip takes the buffer off screen. While a surface
fills an output this way, its dmabuf surface feedback leads with a scanout
tranche (#64): the primary plane's opaque formats and modifiers that the
renderer also imports, targeted at the plane's DRM device, so the client
allocates buffers that plane can show.

Composited frames are 8-bit XRGB8888 unless an output rule asks for
`bitdepth: 10` (#187). Ten bits are opt-in per output, as in Hyprland,
because XR30 primaries break screen sharing and some fullscreen games on
some drivers, NVIDIA's among them. A 10-bit output allocates its buffers in
the first opaque 10-bit format its primary plane scans out and the renderer
draws, XR30 and then XB30 (NVIDIA's planes offer only XB30, and its GBM
cannot allocate XR30 at all; #215), with the plane's modifiers for that
format, and test-commits the modeset first; a plane, allocator or kernel
that refuses it leaves the output at 8 bits with the refusal logged.

A boot splash hands its last frame over without a black one in between
(#204). An output keeps the CRTC its connector is routed to, and the primary
plane already scanning out on it, when the encoder allows them. If that CRTC
is active in the output's chosen mode and its primary plane shows a
framebuffer, the output is inherited: nothing commits to it, test commits
included, until its first frame, which only swaps the primary plane's
framebuffer (with the cursor and colour state, tested as part of it) and
carries no `ALLOW_MODESET`. Any other lit state, a different mode included, is modeset as
before. The compositor draws that first frame only once a client surface has
content on the output (#219), so the splash gives way to the greeter's first
frame rather than to lys-wm's empty background. If no client draws within
`Config.InheritTimeout` (10 s), lys-wm logs it and draws over the inherited
frame anyway, so a greeter that never starts cannot freeze the display. The splash leaves its framebuffer on screen by closing it with
`DRM_IOCTL_MODE_CLOSEFB` (Linux 6.8) rather than removing it.

## Content updates

Each `wl_surface.commit` makes a content update (#117). Extensions attach
what they need to the update when it is committed: gates it must pass
before it applies (an explicit-sync acquire point, a fifo barrier, a commit
timing target) and state that becomes current with it (a release point, an
alpha multiplier, a pointer constraint region). Updates apply strictly in
commit order, so a held one holds back the ones behind it rather than being
overwritten by them, which is what lets fifo queue frames instead of
dropping them. A synchronized subsurface keeps one cached update that
absorbs its commits and enters its queue when the parent applies.

Every surface state that has not applied yet (pending, a synchronized
cache, a queued update) locks the buffer it attaches, and the update
unlocks it once the compositor has taken its contents (#202). A client may
destroy a `wl_buffer` that such a state still holds: the shm pool mapping
or dmabuf planes behind it are released only once the wl_buffer is gone
and no state holds it, so a held update still shows what was attached.
Applied state does not lock its buffer, so destroying the buffer a
surface shows behaves as it did before: shm contents stay uploaded, and a
dmabuf leaves the screen.

A frame latches what it draws: the presentation feedback of every surface
it shows moves from the surface to that frame, and the output's next vblank
reports it presented (#121). An update that applies after the latch, as a fifo
waiter does, neither discards the latched feedback nor is reported for a
refresh that did not show it. The fifo barrier clears at that latch, at once
when no powered output shows the surface, and when an output powers off or
goes away. A commit timing target is due once the earliest next flip of an
output showing the surface reaches it, or at the target time itself, which a
timer guarantees while the outputs are idle.

## Xwayland

`internal/xwayland` (#44) starts Xwayland lazily. At startup lys-wm takes an
X display number with a lock file in `/tmp`, binds `/tmp/.X11-unix/X<n>` and
the abstract socket of the same name, and exports `DISPLAY`. The first
connection launches Xwayland with both sockets as `-listenfd`, a socketpair
as its Wayland connection (`WAYLAND_SOCKET`), another as `-wm`, and
`-displayfd` to learn when it is up; Xwayland then accepts the pending
connection itself. Sessions without X clients pay nothing, and after a crash
the sockets stay with lys-wm, so the next connection restarts it. Starting at
login instead would be a small change on top, which is why the choice was
cheap to settle.

Xwayland listens on its sockets only once a window manager owns `WM_S0`, so
lys-wm's window manager connects over the `-wm` socket as an X client with
`jezek/xgb`, built from a copy in `third_party/xgb` whose extension
initialisation cannot race the connection's reader (#120). X requests block until Xwayland answers, and Xwayland makes
Wayland roundtrips to lys-wm while answering, so that connection lives in its
own goroutine and reaches compositor state only by posting to the loop.
`xwayland_shell_v1` is visible to the Xwayland client alone and maps the
`WL_SURFACE_SERIAL` of each X window to its `wl_surface` on commit.

With zero scaling (#47), the Xwayland client alone sees each output in
physical pixels with scale 1, placed at its layout position times the
largest output scale, so outputs never overlap in X space whatever their
scales. A window's X geometry is its layout rectangle mapped through the
output it is on, and its surface tree is drawn and hit at that output's
scale, so the X buffer lands pixel for pixel and pointer coordinates reach
Xwayland in X pixels. The output is the one nearest the window's corner, so a
window partly off the layout keeps its scale (#163). A surface tree's origin
is a logical point plus an offset in the tree's own pixels; an X window's is
its output's position plus its X position less the output's X origin, so on
that output its buffer covers exactly its X rectangle (#165). Logical sizes
are whole pixels and cannot hold every X size at a fractional scale, so a
floating X window floats within its `WM_NORMAL_HINTS` limits converted to
logical pixels (#167) and is configured within them in X pixels (#164).

`CLIPBOARD` and `PRIMARY` are bridged both ways (#46). When a Wayland client
sets a selection, the window manager takes the X selection with targets
interned from the offered mime types (`text/plain;charset=utf-8` is
`UTF8_STRING`, `text/plain` is `TEXT`, other types keep their name) and
answers X requests by reading the Wayland source. When an X client that has
focus takes a selection, XFixes reports it, the window manager reads its
`TARGETS` and offers the matching mime types as a Wayland source. Transfers
larger than 64 KiB use INCR in both directions, and every pipe has a 30
second deadline. Drag and drop between X and Wayland is not bridged.

## IPC

`lys-wmctl`, bars and scripts talk to lys-wm over a Unix socket at
`$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock`, exported as `LYS_WM_SOCKET`
(#31). Each connection sends one JSON request line and gets one JSON response
line; a `watch` request turns the connection into an event stream of JSON
lines. Every response carries the protocol version.

The server follows the threading rule: a goroutine per connection, which
posts its query or action to the loop and waits for the answer. Events are
not raised at the places that change state. Before the loop waits, it takes
a snapshot of outputs, workspaces, windows, submap and keyboard layout,
and diffs it against the previous one, so no change goes unreported. Only
dispatches are reported where they happen, because they leave no trace in
the state. With no watcher connected, nothing is snapshotted. A watcher
that falls 256 events behind is disconnected rather than buffered without
bound. [`docs/ipc.md`](ipc.md) has the protocol.

## Portals

lys-wm ships no portal backend of its own (#57). It sets
`XDG_CURRENT_DESKTOP=lys-wm`, and the package installs
`share/xdg-desktop-portal/lys-wm-portals.conf`, which xdg-desktop-portal reads
for that desktop:

| Interface | Backends, in order |
| --- | --- |
| ScreenCast, Screenshot | lys-shell, then xdg-desktop-portal-wlr |
| everything else | lys-shell, then xdg-desktop-portal-gtk |

lys-shell's portal is the shell the session runs anyway. It implements every
portal interface without depending on a compositor: it captures through
ext-image-copy-capture or wlr-screencopy (#53), names windows by their
ext-foreign-toplevel identifier and outputs by connector name, and drives
RemoteDesktop through the virtual keyboard and pointer protocols (#52).
xdg-desktop-portal-wlr speaks wlr-screencopy, so screen sharing still works
without lys-shell. GlobalShortcuts needs `hyprland-global-shortcuts-v1` in the
compositor (#89).

## Packages

Everything lives under `internal/` except the commands.

| Package | Owns |
| --- | --- |
| `cmd/lys-wm` | The compositor binary: builds the loop, server and backend, and runs them |
| `compositor` | Connects backend events to the server, places windows and draws each output's frames; `cmd/lys-wm` and the harness both build on it |
| `cmd/lys-wmctl` | The IPC client (#31) |
| `wire` | The message codec and fd passing (#3), and the object runtime generated bindings build on (#4) |
| `scanner` | Generates `protocol/{server,client}/...` from the XML pinned in `protocol/protocols.json` (#4) |
| `protocol/...` | Generated bindings, one package per protocol and side; never edited by hand |
| `loop` | The event loop: fd sources, timers, signals, idle queue, mailbox (#5) |
| `server` | Display socket, clients, object maps, globals; sandbox sockets whose clients carry a security context and do not see restricted globals (#6, #118) |
| `securitycontext` | `wp_security_context_manager_v1`: Flatpak and other sandbox engines register a socket for their apps; the compositor restricts screen capture, data control, foreign toplevels, output management, virtual input, session lock, layer shell, workspaces and global shortcuts, gamma and colour-matrix control and input methods to unsandboxed clients (#118, #123, #124, #176) |
| `wlclient` | The client side of a connection: object map, zombies and dispatch, for the nested backend and tests (#6, #9) |
| `backend` | The backend interface: outputs, frames, input devices (#8) |
| `backend/headless` | Virtual outputs and injected input, for tests (#8) |
| `backend/nested` | Outputs as windows on a parent Wayland compositor (#9) |
| `backend/drm` | Atomic KMS on one DRM device (#40) |
| `render` | The renderer interface (#7) |
| `render/soft` | The CPU renderer, for headless and golden tests (#7) |
| `render/gles` | The GLES2 renderer (#34) |
| `render/vulkan` | The Vulkan renderer, on lys-lib's `gpu` package (#155) |
| `render/rendertest` | The scene and checks every renderer is held to the software renderer's pixels with |
| `gpu` | purego bindings for EGL, GLES2 and GBM (#33) |
| `surface` | `wl_compositor`, `wl_surface`, `wl_region`, `wl_subcompositor`, `wp_viewporter`, `wp_fractional_scale_v1`, `wp_presentation`: double-buffered state, a queue of content updates that apply in order once their gates open, subsurface trees, roles (#11, #19, #117) |
| `tearingcontrol` | `wp_tearing_control_manager_v1`: the vsync or async presentation hint, which travels with its content update; with `tearing` on, a fullscreen direct scanout of a surface that asks, that a rule allows, or that hints nothing and is a game, goes out as an async page flip of the primary plane's `FB_ID` alone (#122) |
| `contenttype` | `wp_content_type_manager_v1`: a surface's none, photo, video or game tag, latched with its content update; IPC reports it per window, and a game counts as asking to tear when it gives no tearing hint (#153) |
| `fifo` | `wp_fifo_manager_v1`: a barrier an update sets when it applies and the next latching deadline clears, which a later update can wait for (#117) |
| `committiming` | `wp_commit_timing_manager_v1`: an update waits until the next refresh of an output showing it would not present it before its target time (#117) |
| `gammacontrol` | `zwlr_gamma_control_manager_v1`: one client per output sets its gamma ramps, which the drm backend tests and writes to the CRTC's `GAMMA_LUT`; the identity returns when the client lets go (#123) |
| `colorrep` | `wp_color_representation_manager_v1`: a client states a YCbCr surface's coefficients (BT.601, BT.709, BT.2020), range and chroma siting; the compositor imports its dmabufs with the matching EGL hints instead of guessing BT.709 from 720 lines up and BT.601 below. Only premultiplied alpha is advertised, and coefficients or siting on an RGB buffer are a pixel_format error at commit (#189) |
| `ctmcontrol` | `hyprland_ctm_control_manager_v1`: the first manager bound sets outputs' 3×3 colour transform matrices on commit, as hyprsunset does, written as S31.32 to the CRTC's `CTM`, which sits before `GAMMA_LUT` so both apply; later managers get `blocked`, and every output it changed returns to the identity when it goes. An output whose CRTC has no `CTM` keeps its colours and the request is reported (#176) |
| `singlepixel` | `wp_single_pixel_buffer_manager_v1`: one-pixel buffers of a solid color (#20) |
| `region` | Pixel regions as disjoint rectangles, for damage and input and opaque regions |
| `output` | `wl_output` and `zxdg_output_v1`: each output's mode, logical position and scale in 120ths, with rounding that keeps logical edges adjacent in device pixels (#19) |
| `outputmanagement` | `zwlr_output_manager_v1`: outputs as heads with their modes for display configuration tools, whose configurations override the config file's output rules until a reload (#107) |
| `shm` | `wl_shm` pools and buffers, read fault-safely when a client truncates its pool (#12) |
| `inputmethod` | `zwp_text_input_manager_v3` and `zwp_input_method_manager_v2`: text inputs follow the keyboard focus, the enabled one activates the seat's single input method, and its text, preedit and deletions reach that text input; the input method's keyboard grab takes the keys pressed while it holds it, and its popups are drawn under the text cursor (#124) |
| `seat` | `wl_seat`, keyboard, pointer and touch, `wp_cursor_shape_v1`, and touchpad gestures through `zwp_pointer_gestures_v1`, which stay with the surface they began on (#13, #14, #108) |
| `data` | `wl_data_device_manager` and `zwp_primary_selection_device_manager_v1`: the clipboard and primary selection offered to the keyboard focus, and drag and drop (#18) |
| `xcursor` | Xcursor files and theme lookup through `XCURSOR_PATH` and `Inherits`, and the built-in arrow (#14) |
| `xkb` | Keymap compilation, sealed keymap fds and modifier state through libxkbcommon (#13) |
| `shell` | xdg-shell and layer-shell glue between clients and `wm`: toplevels, popups placed by their positioner, explicit popup grabs, server-side decorations (xdg-decoration, and KDE's server-decoration for GTK3 clients, both always server-side; #180), activation tokens, and xdg-foreign handles that make another client's toplevel, such as a portal dialog, a child of the exported window, and xdg-dialog's modal hint; focus that would land on a window with a mapped modal dialog goes to the dialog, for X windows with `_NET_WM_STATE_MODAL` too (#15, #16, #17, #20, #48, #110, #111); `xdg_toplevel_icon_v1` icons, copied out of their shm buffers when assigned and latched with the toplevel's commit, for IPC and the tab bar (#154) |
| `wm` | The pure core and the dispatcher (#21, #66) |
| `anim` | Easing curves, springs, tweens and the per-event animation table (#223) |
| `binds` | Typed chords (modifiers with a keycode from the first layout, a button or a scroll direction), submaps and bind flags, matched to `wm` actions (#29) |
| `xwm` | The X11 window manager for XWayland (#45) |
| `config` | The typed config, its validation and reload (#30) |
| `ipc` | The IPC socket and event stream (#31) |
| `session` | logind, udev and libinput: device access, hotplug and VT switching (#38, #39, #42); the `handle-lid-switch` inhibitor, logind's Suspend, and the power supplies' AC state for the lid policy (#179) |
| `harness` | The compositor-in-the-loop harness (#10) |
| `testclient` | A scripted Wayland client that maps toplevels and animates shm frames, for integration tests |
| `testenv` | Whether a test skips or fails when a dev-shell tool or library is missing |
| `golden` | Golden-image comparison for software-rendered frames |

Dependencies point inward: `wm` and `anim` import nothing from lys-wm; `wire` and
`loop` import nothing from lys-wm; `protocol/...` imports only `wire` and
other protocol packages of the same side, since extensions name core
interfaces;
backends and renderers do not import `server`, `shell` or `wm`. `cmd/lys-wm`
is the only place that knows every package.

## Protocol bindings

`internal/protocol/protocols.json` pins every protocol source and maps each
XML file to a package. `go run ./internal/scanner -fetch` refreshes the
vendored XML from it; `go generate ./internal/scanner` regenerates both
sides, and a test fails when the committed tree is stale.

Each interface becomes a type wrapping `wire.Object`. Incoming messages
(requests on the server, events on the client) go to a per-interface
`Handler` with one typed method each, the object first; object arguments
are looked up and type-checked before the handler runs. A message that
creates an object hands the new object to the handler, which returns that
object's handler, so no object exists without one. Outgoing messages are
typed methods, and sending one newer than the bound version is an error,
never a silent drop. The server's client and the client-side display
implement `wire.Peer`: the object map, id allocation and removal.

## Devices

Nothing assumes a single GPU. Each DRM device owns its own `backend/drm`
instance and its own renderer. The primary render device is an explicit
choice passed to whatever needs it, never an implicit global. Multi-GPU
itself lands in M9 (#62), which records the selection rule here.

## Testing

- Unit tests for the pure core and every package with logic of its own.
- Golden images, byte-exact, from the software renderer.
- The compositor-in-the-loop harness (#10) boots the same compositor
  `cmd/lys-wm` runs on the headless backend, in process, with a private
  `XDG_RUNTIME_DIR`, and drives real clients from the dev shell against it.
  `just headless` runs it with `LYS_WM_HEADLESS=1`, which turns a missing
  client into a failure; it is part of `just gates`, so CI runs it too.

## Reference implementations

Hyprland no longer uses wlroots.
[aquamarine](https://github.com/hyprwm/aquamarine) is its backend library
(DRM/KMS, session, libinput, GBM, headless and nested backends) and
[hyprwayland-scanner](https://github.com/hyprwm/hyprwayland-scanner)
generates its protocol bindings; the wire format still comes from
libwayland-server. Hyprland's `hyprtester` is its compositor-in-the-loop
suite and the reference for #10. Issues link the relevant files at pinned
commits under a **Reference** heading.

lys-wm owns the lid (#179). logind acts only on the lid switch's edge
(systemd#7690), so undocking or unplugging the charger with the lid already
closed never suspended, and a script had to watch udev to do it. lys-wm already
tracks the lid and the outputs, so it holds logind's `handle-lid-switch`
inhibitor in block mode, as GNOME's power daemon does, and applies one policy
on every relevant change: suspend when the lid is closed, no external output
is connected and the machine is on battery. The alternative, leaving the edge
to logind and only re-checking afterwards, split one policy between two owners
whose settings could disagree. `lid_suspend: false` hands the lid back.
A sleep is pending from the moment logind accepts lys-wm's Suspend, or
announces `PrepareForSleep(true)`, until `PrepareForSleep(false)`; the policy
does not ask again in between, so the AC and battery uevents of one unplug
suspend once (#205). A Suspend logind refuses is reported, never fatal.
A power supply that disappears while it is read, as USB-C supplies do on
plug and unplug, is skipped; any other read error is reported and leaves the
power state unknown, so the policy does not suspend (#211).
After a wake the policy runs again once logind's default lid holdoff (30 s)
has passed, so a laptop woken with the lid still closed, undocked and on
battery sleeps again, while a lid opened to wake it, or a dock that comes up,
is seen first; a suspend that fails at once cannot spin (#212).

Licences decide how they may be used:

- Hyprland, aquamarine and hyprwayland-scanner are BSD-3-Clause, libwayland
  is MIT. Their logic may be ported; ported code keeps the original
  copyright notice and licence in `THIRD_PARTY.md`.
- hy3 is GPL-3.0. Read it to pin down behaviour (collapse policy, autotile,
  move semantics); never port its code.
- KDE's `server-decoration` protocol XML is LGPL-2.1-or-later. It is vendored
  unchanged with its licence header and only describes the wire interface,
  as wlroots, sway and Hyprland ship it; no KDE code is ported.
