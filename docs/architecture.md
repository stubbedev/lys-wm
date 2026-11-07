# Architecture

The decisions every change builds on. Scope and non-goals are in
[Scope](./guide/scope); the plan is in the issues. When a decision changes,
change it here in the same commit and say why in the issue that changed it.

## Pure Go

lys-wm builds with `CGO_ENABLED=0`, always. A C library is used only when no
reasonable Go alternative exists, and then it is loaded at runtime with
[purego](https://github.com/ebitengine/purego) (`dlopen`), never linked.

| Need | Source |
| --- | --- |
| EGL, GLES2, GBM | `libEGL`, `libGLESv2`, `libgbm` through purego (#33) |
| Vulkan | `libvulkan` through lys-lib's `third_party/vk` and `gpu` (#155) |
| Input devices | `libinput` through purego (#42) |
| Keymaps and keyboard state | lys-lib's `xkb`, in pure Go |
| Wayland wire protocol | in-repo codec and scanner (#3, #4) |
| DRM/KMS ioctls, pixel formats, EDID | lys-lib's `drm`, `fourcc` and `edid`, shared with lys-splash (stubbedev/lys-lib#30) |
| epoll, timerfd, signalfd, memfd | [`golang.org/x/sys/unix`](https://pkg.go.dev/golang.org/x/sys/unix) |
| logind, systemd | lys-lib's `third_party/godbus`, godbus v5.2.4 with the fd and ordered-dispatch fixes every lys process shares |
| udev | netlink, read directly (#39) |
| X11 window manager | [`github.com/jezek/xgb`](https://github.com/jezek/xgb) (#45) |
| Text | lys-lib's `render`, on its `third_party/typesetting` copy of [go-text/typesetting](https://github.com/go-text/typesetting) |
| Icon themes | lys-ui's `icontheme` |
| Config file | [`go.yaml.in/yaml/v3`](https://github.com/yaml/go-yaml), strict, as lys-shell uses (#30) |

Adding a purego library, or a dependency to this table, is a decision issue.

Keymaps come from lys-lib's `xkb`, which replaced libxkbcommon so that no compositor
needs the C library loaded, a key event allocates nothing, and every lys process
shares one implementation. It is held to libxkbcommon by differential tests across
every layout, variant, option and model xkeyboard-config offers. Keymaps compile in
the v2 format, as `xkb_keymap_new_from_names` does, and clients are served xkb_v1
text, what `wl_keyboard` carries; third-party clients read it with libxkbcommon, and
`TestLibxkbcommonClientsReadAllThreeLayouts` holds it to `xkbcli`. Before that, xkb-go
was dropped (#13) because its RMLVO compiler dropped the compatibility section.
The remaining purego libraries' paths are baked in at build time by the Nix package,
and the release builds load them by soname.

Tab titles are drawn with lys-lib's `render` package, which lys-ui moved
there with everything else that is not UI (stubbedev/lys-ui#357). lys-wm does
not discover fonts, so the tab font is a file: `tabs.font` in the config, else
`LYS_WM_FONT`, else the DejaVu Sans the build bakes in.

## Threading

One goroutine owns all compositor state and runs the event loop (#5). Other
goroutines exist only at the edges, for blocking work such as D-Bus. They
post closures to the loop's mailbox and never read or write state
themselves. The compositor's state takes no lock, with one exception below.
Packages under it keep their own: the loop's mailbox, the IPC server's
connection set, and the process-wide tables in `gpu` and `session`.

A shm pool can be mapped from any file, so reading one can block on a page fault for as
long as its filesystem likes. A pool on tmpfs or hugetlbfs, which is what toolkits use,
is read on the loop while the damage is small. Any other pool, and any commit damaging
more than a million pixels on a surface already showing a buffer, is copied by a goroutine first: the commit holds at the same
gate as an implicit-sync dmabuf, and applies with the copy ready, so what it uploads on
the loop is memory no client can stall. Toplevel icons from other pools are ignored.

Screen capture into shared memory is one such edge (#131). The loop draws
the capture, starts an asynchronous read into a pixel buffer and maps the
client's buffer privately; a goroutine waits on the GPU fence, the loop maps
the pixel buffer, and a second goroutine copies the rows into the private
mapping. Each step hands back to the loop through the mailbox, so the loop
never waits on the GPU or copies a frame. The exception is the read's
atomic `cancelled` flag: a client that abandons a capture sets it without waiting
for anything, however long the copy is stuck on the client's memory, and the copy
checks it before every row, so at most the row in flight lands afterwards. The
mapping is released off the loop too. On close, the compositor waits for these
goroutines and releases what their posted closures would have.

Everything else that waits on another process runs on a worker too. A bind's `exec`
forks there, in order, and a reload reads the file, its font and compiles its keymap
there and only applies the result on the loop; over IPC the connection's goroutine does
that load, so its reply still says whether the config was taken. A hotplug's
A cursor theme file is read on a worker: the common shapes at startup and for each new
output scale, any other shape on its first use while the nearest loaded one shows. A tab
label is rastered on a worker and uploaded between frames while its old texture, or just
its background, stays on screen.
connector probe, which reads EDID over DDC, posts its result to the loop. An input
device is taken from logind or the holder on one worker per session, in order, and
added to libinput on the loop with its fd ready; a release, a VT switch, a completed
pause and the lid policy's calls go the same way. The loop never sends a D-Bus
request once the session is up.

The loop goroutine is locked to its OS thread for as long as it runs
(`gpu.PinThread`, #149), so a GL context it makes current stays current
between renderer calls: `eglMakeCurrent` runs only when another display's
context is current on that thread. On any other goroutine the renderer makes
its context current and releases it around every call, as before, since the
goroutine may move to another thread in between. Unpinning releases the
context, so whatever closes the renderer afterwards can make it current.

The pinned thread asks for SCHED_RR at priority 1, then nice -10, both resetting on fork so
no other thread or child inherits them, and logs what it got. The Debian and RPM packages
and the NixOS module allow that through a PAM limit; a file capability would make the
binary setuid-like to libc, whose secure mode hides the driver environment variables the
renderers need. Without the limit, as with a lys-wm from a Nix profile on another distro
(#277), the thread asks rtkit on the system bus: SCHED_RR first, then nice -10. rtkit
grants SCHED_RR only to a process whose hard RLIMIT_RTTIME is within its RTTimeUSecMax,
so lys-wm lowers the hard limit to that, for itself and its children, and the soft limit
to half of it. A thread that runs past the soft limit without blocking gets SIGXCPU and
drops to SCHED_OTHER for the rest of the session, before the hard limit would kill it.

## Errors that end the session

`Loop.Run` returns the first error a callback returns, and `lys-wm` exits
with it. So only two classes of error may reach the loop (#144):

- **Device gone**: the device is gone and nothing on it can recover.
  `ENODEV` from any ioctl, and `backend.ErrDeviceLost`, which the nested
  backend wraps around a failed send to its host and the compositor
  wraps around a GPU that keeps resetting (below).
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
  cursor refused with EINVAL or ERANGE falls back to the frame (#143): a move
  is one atomic request, with no test commit ahead of it (#259).
- **A client**: a failing request kills that client (`server.Client`),
  and a failed capture sends `failed`, as before.

**A GPU reset** (#374) is not one of them. An amdgpu ring timeout, an i915
or xe engine reset or an NVIDIA Xid loses the GLES context
(`gpu.ErrContextLost`: `EGL_CONTEXT_LOST`, `GL_CONTEXT_LOST`, or a reset
status) or the Vulkan device (lys-lib's `gpu.ErrDeviceLost`), and
`backend.GPULost` names that class. The EGL context asks for
`EGL_EXT_create_context_robustness` with `EGL_LOSE_CONTEXT_ON_RESET`, so a
reset is reported rather than drawn through. Recovery works like a robust
context's own contract:

- A renderer that sees a loss remembers it and turns every later call into
  a no-op that succeeds: resources made meanwhile are recorded, passes draw
  nothing, uploads and reads do nothing. No caller anywhere sees the loss
  as an error, so nothing between the reset and the next frame kills a
  client or disables a feature.
- `Renderer.Lost` is the one place a loss is reported. The compositor asks
  after every output frame and when a frame fails; GLES asks the driver
  with one `glGetGraphicsResetStatus` per call, no allocation.
- `Renderer.Recover` rebuilds in place on the same GPU: GLES renews its
  context, Vulkan opens a new `VkDevice`. Every texture, target and screen
  shader made before keeps its identity and is remade; dmabuf-backed ones,
  client buffers and scanout swapchains alike, are imported again from
  their still-open fds, exported ones from duplicates of theirs. Contents
  are undefined afterwards, so the compositor uploads every shm surface
  again from its pool, drops what it drew itself (labels, cursors,
  gradient stops, grain, thumbnails, blur chains, working targets,
  snapshots, closing animations), invalidates every scene and repaints
  every output whole. Explicit-sync release points need nothing: a lost
  renderer gives no fence, so they signal at once, and a fence taken
  before the reset is a kernel dma-fence, which a reset signals too.
- `gpu.Losses` (lys-lib) bounds it: past three resets in a minute the
  compositor returns `backend.ErrDeviceLost` naming every one, and the
  session ends rather than spin on a GPU that hangs on every frame.
- A secondary GPU's blit renderer and a separate Vulkan copier recover the
  same way inside the DRM backend, bounded on their own; a copier that is
  the primary renderer's device re-imports its buffers once that device
  changes.
- `Renderer.LoseDevice` raises a loss at any point, for tests; the soft
  renderer's fake device clears every uploaded texture when it recovers,
  so a test sees what a real reset loses.

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
events. The compositor keeps a tween per window over its tile, sets it when
a layout change lands, and samples it once per frame at the vblank the frame
aims at, the next in the phase of the output's last presentation (#330), or at
the frame's start time without one (`Config.Clock`, so tests drive time
themselves). A window's draw ops are
recorded at its goal geometry and then mapped onto the tile the animation
shows, so its surfaces, border and popups move as one and the op diff damages
exactly what moved. After drawing, an output schedules its next frame for as
long as an animation overlaps it, whether or not the frame changed anything,
so an animation slower than a pixel per frame still reaches its end.

The default springs are stiff enough that a key-triggered animation reads as
instant (#331): every key-triggered default covers at least 7.5% of its travel
on the first frame at 60 Hz, half by the fourth and 95% by the tenth (the
workspace switch is 900 stiffness at a damping ratio of 1). A spring from rest
barely moves for its first refreshes, and a stiffer one was chosen over giving
key-triggered animations an initial velocity, which would make the same event
move differently depending on what started it.

### Layout changes land together

A layout change that gives windows a new size is a transaction. `reconcile` splits
in two: the configures go out at once, and the placements (`tile`, `rect`, `layer`,
the stack, the tab bars, and the animations they start) are installed only when
the change lands. Until then everything keeps the old presentation, and because
drawing, hit testing and popup placement all read those placements, what is shown
and what takes input stay one state. Keyboard focus and `activated` do not wait.

A window is waited for when it is visible, not minimized, on an output, and its new
size differs both from what it was last asked for and from what it has already drawn.
No such window, no transaction: the change is installed in the same call, so a
focus change, a floating move, a workspace switch to windows at their sizes, or a
lone window nothing happens to, pays one comparison per placement. No hook is
installed, no timer armed, and a frame costs two loads, so an idle desktop stays
asleep. A change that maps a window is not held either (#332): the new window starts
its fade-in on the first frame after it maps, and neighbours that have not yet drawn
their new sizes are stretched until they do, as after the bound, rather than one slow
client delaying the new window. `Config.HoldOpening` brings the hold back.

While it is held, a commit that answers the configure is parked by a
`surface.Barrier`, a hold at the end of the update's gates: it reaches the barrier only
once it is at the head of the surface's queue and its explicit-sync fence, FIFO and
commit-timing gates are open, so a member is ready when its pixels are, not when it
asked. The commit's frame callback stays parked too, so a client does not draw on
ahead. An answer is the ack of the last configure on xdg-shell, with a buffer
attached; a client that commits while the configure is unacked just goes on showing
its old size. A null attach is not an answer and unmaps at once. An X window has no ack: its answer
is a commit at the size it was configured to. A client that speaks
`_NET_WM_SYNC_REQUEST` also has to raise its counter, watched with a SYNC alarm, since
the X server's own repaint of a resized window commits before the client has drawn; a
client that misses a request is not waited for on that counter again until it answers
one. A window that is itself new and not shown yet gets its frame callbacks during the
hold, as Xwayland will not commit the new size before the callback of its last commit.
Subsurfaces in sync
mode are parked with their parent, as the protocol has them; a desynchronized one
updates inside the old frame, which is what it asked for. When the last member is
ready, or at the bound, the placements are installed and every barrier released in
one pass of the loop, so the next frame on every output shows all of it, and the
animations start at that moment. A spring or tween in flight runs on during the hold
and retargets from what it shows, with its velocity.

The bound is 100 ms (`DefaultHoldBound`). It is not a config key: longer feels like a
laggy keypress, and shorter only makes slow clients draw the fallback below sooner.
A client that misses it is marked stalled and nobody waits for it again until it has
caught up, so a frozen client costs one bound, once, not one per change. Anything that
changes the arrangement during a hold (a window opening, closing or unmapping, a
client dying, a workspace switch, an output going away) lands the old change at
once and starts over, so holds do not queue and a key press never waits behind
another one. So a window that closes during a hold does not hold anyone: its
snapshot starts fading at once, since nothing about it needs an answer.

While a client's content does not match its frame, it is scaled to the frame. During
an animation that is the existing remap of the window's ops, new buffer onto the tile
in between: no offscreen pass, nothing to fill or clip, and the border and content
stay one shape. A stalled client past the bound gets the same treatment from its old
buffer: its surface ops alone are stretched onto its new frame (`frame.stretch`), the
mask staying where the frame is, so there is no hole of wallpaper and no old buffer
over a neighbour, at the cost of soft text in a window that is not responding anyway.
It works on every renderer because it only changes where ops are drawn, and costs
one comparison per drawn window. A crossfade from a snapshot looks a little better at
large size changes and costs a texture and a second pass per window, per frame; a clip
shows holes. This is only for a client that has not answered: one that acked and drew
a different size (a terminal rounding to its cell grid, say) said what it wants and is
drawn at its own size, clipped to its frame when it drew larger, so it stays out of the
gaps and its neighbours. Hit testing stays at the client's real geometry.

A drag coalesces. A pointer motion only notes that it happened and allocates nothing;
the next frame moves `wm` state to where the pointer is and reconciles, one configure per
frame however fast the mouse, and never while a change is in flight, so a client draws
what the pointer did last. The hold's own bookkeeping is kept per window and reused,
so a transaction allocates nothing beyond the `wm` state and arrangement it is about, and
costs one `timerfd_settime` for its bound; a drag leaves the timer armed when a change
lands, as the next one re-arms it. A drag step copies only what it changes: the
workspace list and either its floating list or the groups on the way to the window,
sharing the rest with the state before it. The arrangement is built in one of three
reused `wm.Arranger`s, since the one shown and the one held may both still be read.
A drag holds for at most 17 ms (`dragHoldBound`). A client that misses that is not waited
for again until the drag ends, and gets a new size only after it answered the last,
so a slow client lags at its own pace, stretched, while the frame keeps up with the
pointer at the refresh rate and its socket does not fill with configures. A drag
lands what is in flight when it starts, since it measures from what is placed.

A client commit runs a frame only on the outputs whose last frame drew a member
of its surface tree, unless the commit may reach another: its tree's bounds, the
role's geometry or popup position, a surface offset or a surface gaining or
losing its texture changed, the surface is new, or its role does not state its
placement (cursors, drag icons, input method popups). A missed wake is a stale
picture, so anything not known to stay put wakes every output.

A window that appears starts from its `windows_in` style and fades in; its
ops are scaled by the fade, borders and shadows included (#231). A closing
window is gone from the window list at once, so it takes no input and leaves
the layout; what plays out is a snapshot: its surface tree and border drawn
into an offscreen texture target at unmap, or at the null attach that unmaps
it, whichever comes first, while its textures still exist. The snapshot is
drawn above the windows of its output until its animation ends, and dropped
then without a frame of its own.

## Dispatch

One typed `Action` set and one dispatcher, in `wm` (#66). Keybinds,
`lys-wmctl dispatch`, the IPC socket and protocol requests such as
foreign-toplevel activate are all callers of it. Nothing else mutates WM
state.

## GPU

GBM and EGL (and Vulkan) get their own file on the device, the render node where there is one, or a second file on the card where the stack cannot allocate on it (Mesa's software GBM needs dumb buffers, which a render node refuses): GEM handles are per file and per buffer, so on one file Mesa and the compositor share a handle and either closing it pulls it from the other. KMS imports close their handles as soon as the framebuffer holds its reference.

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
it on every backend, and it is a peer of GLES in every path. On the DRM backend
GBM allocates scanout targets in a modifier the plane and the device share,
Vulkan imports them, and sync files fence them: the kernel refuses framebuffers
over memory Vulkan exports on `xe` (Lunar Lake), in every modifier, and takes
the same modifiers from GBM. Beyond how they open, the two renderers ask the same
of a card, so one `gbmAllocator` holds either. On the nested backend
nothing scans out directly, so Vulkan renders into memory it exports itself, on
the device the host names in its dmabuf feedback, and needs no GBM. A host below
`zwp_linux_dmabuf_v1` version 4 names no device, as lavapipe's does: Vulkan then
opens the default device and offers what the host listed, while GLES, which
allocates through GBM on that device, reports what it lacks. Headless Vulkan
tries the render nodes in order before any other device, so that it has a main
device to name as headless GLES does, which dmabuf feedback, dmabuf captures and
explicit sync all need.

GLES stays the default, since not every GPU has a good Vulkan driver. lys-lib
loads `libvulkan.so.1`, whose store path the Nix build bakes in and
`LYS_LIBVULKAN` overrides; the dev shell points `VK_DRIVER_FILES` at lavapipe,
as it points EGL at llvmpipe, and both renderers are pixel-compared against the
software renderer by `render/rendertest`, which also holds the checks both must
pass on real GPUs. Lavapipe shares dmabufs only through `/dev/udmabuf`, so the
paths that share them run on it in the `vm-gpu-tests` NixOS check; `just docker
hardware` runs them on a real GPU, on both renderers.

Without cgo, `os.Setenv` does not reach libc's `getenv`, so variables that
drivers read must be in the process environment from the start. The dev
shell sets `__EGL_VENDOR_LIBRARY_DIRS` to Mesa's vendor and
`LIBGL_ALWAYS_SOFTWARE=1`, so GPU tests render on llvmpipe the same way
locally and in CI. Run them with both unset to exercise the machine's
driver: the dmabuf test only runs where a vendor drives a render node.

The pointer rides a cursor plane where the output has one (#83). The plane
takes a linear ARGB8888 dumb buffer, the only buffer every cursor plane
takes: NVIDIA's cursor planes scan out linear only, and its EGL renders to no
linear modifier. A theme cursor or a 1:1 shm cursor surface is copied into it
from CPU pixels. A cursor that needs drawing, scaled or transformed, is
rendered on the GPU and read back off the loop where the renderer reads
asynchronously; the software renderer draws straight into the dumb buffer;
a GPU renderer without async reads leaves such a cursor in the frame rather
than stall the loop on a readback (#271). Pointer motion then moves the plane in an atomic commit of
its own, or in the next frame's commit, and repaints nothing. A move waits
for a requested frame only when that frame is due before the next vblank; a
frame deferred past it (#257) does not hold the cursor back a refresh (#276).
A cursor-only commit is sent at the plane lead before the vblank; a latch
that fires late, even past that vblank, commits at once for the next one
rather than aiming a refresh further on (#340).
A cursor commit
sets only what changed against what the last accepted commit left on the
plane; a modeset, a resume or a cursor coming back from hidden sets it all. An image
larger than the plane, or a plane that refuses it, puts the
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
The blit does not care what renders on either side. A Vulkan session runs its
secondaries on Vulkan too, and a card without a Vulkan driver takes GLES behind
a Vulkan primary, since all they exchange is a dmabuf and a sync file. NVIDIA
renders into no linear buffer on either API, so an NVIDIA primary renders
its usual tiled frame and its Vulkan device (lys-lib's `gpu`) copies it with
the copy engine into a linear buffer its GBM allocates, which the secondary
samples (#147); sync files order the three steps. A Vulkan primary makes that
copy on its own device and queue, so the frame needs no fence to reach the copy.
The startup log names the renderer each secondary blits with. Outputs keep
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
allocates buffers that plane can show. A scanned-out frame keeps the scene's diff
base, so a commit that changes nothing commits nothing; only the composited
targets count as stale, and the next composited frame repaints them whole.

The buffer need not match the mode (#379): a `wp_viewporter` source and
destination go to the primary plane's `SRC_*` (16.16) and `CRTC_*` rectangles, so a
game rendering below the panel's resolution scans out scaled. A fullscreen
surface smaller than its output is centred over a black border fill, as
xdg-shell suggests, and that letterbox scans out too, the CRTC showing black
outside a primary plane that does not cover it. A driver that cannot scale or
needs a full-screen primary refuses in the test commit, cached per buffer and
rectangles, and the frame is composited. An async flip changes only `FB_ID`, so
a tearing surface whose rectangles change takes one vblank flip first.

Opaque, untransformed, unrounded windows of at least 256x256 go to overlay
planes (#380), up to three per CRTC (`backend.MaxOverlayPlanes`), topmost first,
scaled and cropped by their viewport as the primary is. Since KMS overlays sit
above the primary, a candidate must have nothing painted over it, judged by
what the ops above paint rather than their bounds, so a border ring around a
window does not disqualify it. Each frame makes at most one test commit: a new
layer is tested alone, then the set together, and a set the planes refuse
together drops its lowest layer; every result is cached. A frame in which only
overlaid surfaces changed flips the overlay planes and composites nothing. A
surface that is a candidate for two frames running is offered the overlay
planes' formats as its dmabuf scanout tranche, and loses it after two frames
out, so a client allocates what a plane takes without the feedback flapping.

Composited frames are 8-bit XRGB8888 unless an output rule asks for
`bitdepth: 10` (#187). Ten bits are opt-in per output, as in Hyprland,
because XR30 primaries break screen sharing and some fullscreen games on
some drivers, NVIDIA's among them. A 10-bit output tries each opaque 10-bit
format its primary plane scans out and the renderer draws, XR30 and then XB30
(NVIDIA's planes offer only XB30, and its GBM cannot allocate XR30 at all;
#215), with the plane's modifiers for that format: it allocates the buffers
and test-commits the modeset, and a refusal moves on to the next format with
the refusal logged, because a plane's format list does not say what the
allocator or the kernel takes. Only when every format is refused does the
output stay at 8 bits on the frames it already shows, with each refusal
logged. Scanout memory stays GBM's on
both renderers: the formats a plane offers on NVIDIA allocate there, so a
fallback to Vulkan-exported memory, which `xe` refuses, would have nothing to
serve.

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
`Config.InheritTimeout` (30 s), lys-wm logs it and draws over the inherited
frame anyway, so a greeter that never starts cannot freeze the display. The splash leaves its framebuffer on screen by closing it with
`DRM_IOCTL_MODE_CLOSEFB` (Linux 6.8) rather than removing it.

A frame's damage is what differs from the last frame's ops. The two op lists are
aligned, equal prefix and suffix first and then a bounded look ahead, and only ops
outside the alignment damage their rectangle: any common subsequence is correct,
as a pixel no unmatched op touches has the same stack of ops in both frames. An op
split around what covers it is one group keyed by what it draws, so a group that
matches damages only the difference of its coverage. Surface damage is clipped by
the opaque ops above it, and an opaque region counts as opaque, so a commit nobody
can see damages nothing. A damage set past 32 rectangles merges the pair that costs
the least area rather than collapsing to their bounds.

Every draw takes a coverage mask (#225): a rounded rectangle to keep and one to
cut out, in target pixels, each antialiased by its signed distance at the pixel
centre. lys-lib's `gpu` owns the formula once, as GLSL that compiles both as
GLSL 4.50 for its Vulkan pass and as GLSL ES 1.00 for the GLES renderer
(`gpu.MaskGLSL`), and as `gpu.Mask.Coverage` in float32 for the software
renderer, so the three stay within the parity tolerance. Rounded windows draw
their surfaces through their window's mask and their border as one ring.
A masked op covers only its solid part, the keep rectangle less its corner
squares, so culling still drops what lies under a window's middle, and direct
scanout never takes a rounded window.

Effects draw part of a frame offscreen and then draw it back (#226).
`Renderer.NewTextureTarget` makes a target a later pass samples as a texture:
the software renderer's buffer itself, a GLES framebuffer's colour texture, and
a lys-lib offscreen target whose image the next sampling pass moves to its
shader-read layout. Passes stay one at a time, so an effect is a pass into its
target followed by the frame that samples it; a pass refuses to draw its own
target, and the target's texture takes no uploads.

Blur (#234) renders the ops already recorded under a window, within the
blur's reach, into an offscreen target, halves it through `passes` dual Kawase
steps and doubles it back, and draws the result under the window through its
mask. The blurred ops and their surfaces' pending damage are kept per window.
When what lies below changes, coverage or settings, the chain is redrawn whole
and the blur op's version changes, so the op diff repaints the whole window.
When only content below is damaged, each level is refiltered only over that
damage grown by the blur's reach and halved per level, growing coarse to fine
by a Kawase tap so no level reads a stale neighbour, and the blur op keeps its
version and adds that grown damage to the frame's.

Every blur is one `drawBlur`, whatever asked for it: a shape in device pixels,
drawn as one op per rectangle, sampling one chain. `decoration.blur` asks for
a window's or a listed layer's rectangle, or a popup's window geometry. A
client's `ext_background_effect_v1` region asks at the surface it belongs to,
as the surface tree is drawn, so a subsurface's blur sees its parent, and it
replaces that surface's whole-surface blur. Chains belong to the output's scene,
keyed by surface (one shared per output under `xray`); a frame that draws no
blur for a surface frees its chain, so an effect that is off holds nothing and
costs one map lookup per surface. A chain's area is rounded up to the steps
textures grow by and moved within the output while the shape still fits, so a
shape that grows or moves reallocates a bounded number of times, and the
up-sampling levels write back into the down-sampling targets. A window whose
blur area overlaps nothing drawn above the background, and any tiled window
unless `Blur.LiveTiled` is set (Hyprland's `new_optimizations`), blurs over the
output's shared background chain, so a workspace of translucent tiled windows
filters its background once, whatever slides or resizes over it.

`drawBlur` records what it needs, an op that names a pending chain, and the ops
below it. Only once the whole frame is built and culled does `resolveBlurs` draw
the chains that a surviving op still needs, in stacking order, so one chain's
output reaches the next through its ops. A surface behind an opaque window draws
no chain and keeps none; the ops under a chain are culled the same way before
they are compared, so what an opaque window hides from a blur cannot redraw it;
and a shared chain is drawn once however many surfaces use it. A deforming
window replays its ops into a target as it is drawn, and resolves its own chains
first.

The look (`wm.Look`) is built from what a renderer already has wherever it can
be. A tint is a masked fill. Grain is lys-lib's `render.GrainTile`, the noise of
`Canvas.PaintGrain`, drawn with `DrawTexture` at the strength's alpha, so a
shader needs no integer hash GLSL ES 1.00 cannot express and the pixels stay
within two levels of lys-ui's: one tile per compositor, the size of the largest
output, built at the first use. A rim is four strips of a ring mask filled with
a gradient, whose line turns with the pointer in 64 steps, so damage is the
strips, and the pointer asks for a frame only when a step changes; a static rim
needs no pointer at all. Saturation, brightness, contrast and refraction need a
shader: `Pass.DrawGlass`, with `lgpu.GlassGLSL` the one source for GLES and
Vulkan. The software renderer takes refraction from `render.RefractPlan.At`, which
is lys-ui's arithmetic and the shared definition; the shaders' float form is
tested against it, within 3 of 255 in `TestDrawGlassMatchesGo` and in `Parity`,
which also holds the colour maths to the filter functions'. Its program and
pipeline are built at the first draw, and the op stays a plain `DrawTexture`
when none of the four is set.

On an HDR output a chain is a working target, so the look runs on linear light:
the colour filters clamp at SDR white, and a glass with no colour change leaves
light above it alone. Their numbers mean what they do in SDR only roughly, since
they were defined for encoded values.

Mica is that look over a shared chain of the layers below the windows. When its
size and passes match `decoration.blur`'s it is `xray`'s chain, or else one of
its own, so one per output either way. The chain is opaque, so a mica op at full
alpha culls the windows below it like any opaque op.

A closing window's or layer's snapshot keeps the shape, mask and look it had
(`frost`) and draws it through the same `drawBlur` under the snapshot. Like a
window's opening animation, the frost samples the backdrop at the window's place
and is scaled with the snapshot, so a closing window redraws no chain unless
what is behind it changes. A minimize's genie cuts the snapshot into strips and
keeps no frost. The offscreen frames behind snapshots and window captures draw
no blur: they have nothing behind them to blur.

The overview draws the windows it moves as they are: their ops, remapped from tile
to slot as any window animation does, because a live window is already a texture
and a screenshot of it would be one more texture drawn just as often. 30 floating
1000x700 windows moving cost the same draws either way. What it does cache is
thumbnails: one target per workspace, holding the workspace as it looks on screen
(the `background`, `bottom` and `top` layers and its windows, fullscreen ones above
`top`, but not `overlay`, which the overview draws live above everything). A
thumbnail redraws only when a window in it or one of those layers commits damage, so
it is one op in the frame. A redraw draws the workspace at full size and
halves it with the blur chains' Kawase step down to the thumbnail, the one filter
every renderer has, because a straight draw at a seventh of the size turns fine
lines into moire on all of them. Every redraw takes that path, the first and every
one after a commit, so a clock ticking in a thumbnail never swaps the filtered image
for a straight one. That costs a full-size draw per redraw, so an output redraws one
thumbnail a frame, after the frame that found it stale so that frame shows the old
image instead of waiting on the new, and a thumbnail waits four times its last
redraw before the next, which holds redraws to a fifth of the loop however slow the
renderer. A bar that commits often dirties all of its
output's thumbnails and they take turns within those limits; an idle one costs nothing. All windows blur over one shared chain of the
layers below them while it is open, since their own chains would redraw whenever
any window moved.

A screen shader is the last of these: an output's ops replay into a retained
image with the scene's damage, and one pass draws the whole output from it through
the shader. Its language lives in `internal/shader`, a parser and type checker
for a GLSL ES 1.00 subset with no dependencies, because one source has to run
on three renderers with no compiler library at runtime: GLES gets the checked source,
Vulkan gets SPIR-V emitted straight from the typed tree with no optimiser
(`spirv-val` and glslang check both in tests), and the software renderer gets a
tree of closures over a register file. Function calls are static, with no stack,
since a function can only call those above it. Capture replays the scene's ops as
it always did, so it never passes through the shader, and a locked session
bypasses it.

## HDR

HDR is per output and off by default; off, no pass ever calls `SetColor`, no
shader variant is compiled, no target is float, and the display list carries a
nil pointer per op, which `replayFrom` compares once. The SDR shaders' SPIR-V is
byte for byte what it was, and the SDR goldens are unchanged.

Colour maths is `colorspace`: pure functions from a `Description` (the protocol's
image description) and a `Target` to an `lgpu.ColorTransform`, which every
renderer evaluates: `gpu.ColorGLSL` in GLES and Vulkan, its Go mirror `Apply` in
software, on CPU for fills and in the shader for textures and gradients.
`rendertest.ColorParity` holds them to each other.

A frame on an HDR output draws twice. The scene draws into a working target kept
per output, in half floats: linear BT.2020, 1 at the output's SDR white, damage
only. Then one draw encodes the repaint through PQ into the 10-bit swapchain. So
SDR content is decoded as gamma 2.2 on sRGB primaries and tagged content by its
description, each draw anchoring its reference white at 1 and mapping black to
black. A source brighter than the panel compresses into the peak on its largest
channel above 3/4 of it, so hue holds and everything below is untouched; colours
outside the destination primaries clip. Passes into SDR targets, an SDR output
showing HDR content and every capture, run the same transforms to gamma 2.2 with
1 as the peak; blending stays in gamma there, as it always was.

Blur, shadows, rounded corners and snapshots follow the pass they are in: on an
HDR output their targets are working targets, so Kawase averages light and
coverage multiplies light. A working target's texture says so (`Working`), so a
capture's transform takes it to SDR like anything else. `blur.go` only asks
`newSceneTarget` for its chain and rebuilds it when the output's encoding changes.

KMS: `Commit` tests the modeset with `Colorspace` BT2020_RGB, `max bpc` 10 and
`HDR_OUTPUT_METADATA` built from the EDID's CTA-861 HDR Static Metadata block
and base block primaries, and any refusal leaves the output SDR with a report. A
session that never used HDR touches none of these unless the previous session
left them set. The cursor plane, gamma LUT and CTM act after the encode and would
act on PQ, so on an HDR output the cursor draws in the frame, gamma clients get
`failed`, and the colour matrix moves into the encode.

A working target starts transparent and `colorApply` turns non-finite values into
nothing, because a NaN blends through every later draw over a target that is
never fully repainted.

Nothing here has run on an HDR display. Connector programming is tested against
fake KMS and the pixel paths on lavapipe and llvmpipe.

## Tiled displays

A monitor driven as tiles (some 5K and 8K panels, MST tiling) gives each
connector a `TILE` property naming its group, the grid and its place in it.
When every tile of a group is connected and no member already lists a mode of
the whole picture, the group is one output: the connector at the top left
names it, its one mode is the tile mode grown to the grid at the same rate, and
its frames are one buffer of the whole size. Each tile keeps its connector, CRTC
and primary plane; every commit sets all of them together, each plane scanning
its tile's part of the one framebuffer, so the tiles flip as one frame, and the
frame is presented when every CRTC's flip event has arrived. Gamma, the colour
matrix, the HDR signal, the content type and adaptive sync go to every tile.
A tiled output has no cursor or overlay plane, which would show on one tile
only, and never flips async, which commits one CRTC. A group whose driver drives
the whole picture on one connector (Aquamarine's redundant tiles) lights that
connector alone; an incomplete group lights each connector as its own output,
and a tile coming or going rebuilds the output.

## Variable refresh

Decided in #298: lys-wm supports adaptive sync, off by default. scope.md promises
games, and a fullscreen game below the panel's rate is where a fixed refresh
judders; HDR, the other #40 non-goal, landed, and VRR needs nothing it lacks.

An output rule picks `off`, `on` or `fullscreen` (Hyprland's `vrr` 0, 1 and 3).
`fullscreen` follows the frame hints that already set the connector's content
type and the tearing decision: one surface alone filling the output, a game or a video
by its `wp_content_type_v1` tag or a rule's `content` key (#378). The compositor asks the backend through `backend.AdaptiveSync` on
each frame it draws, and the DRM backend sets the CRTC's `VRR_ENABLED` only when
it changes or on a modeset, never in an async flip. Capability is the
connector's `vrr_capable` on a CRTC with `VRR_ENABLED`.

While it is on, nothing assumes a fixed interval: the frame clock starts a frame
as soon as it is asked (no latch half a refresh before the next vblank), the
planes commit at once, no frame is timed against an aimed vblank, and
animations sample at the clock instead of a predicted vblank. Presentation
feedback reports refresh 0 to `wp_presentation` version 1, which cannot say
otherwise, and the mode's interval, the fastest the output goes, to version 2.
A cursor move commits at once on a VRR desktop, where the cursor is the
content, but not while one surface fills the output (#373): each commit starts a
refresh, so a 1000 Hz mouse would drive the panel instead of the game. The
compositor states that frame hint through `backend.FrameFilling`, and the cursor
rides the next frame's commit, committing on its own only when no frame came
within the panel's longest frame, the period of the minimum refresh its EDID
states (`edid.ParseVRR`), else of 24 Hz, as Hyprland's `cursor:min_refresh_rate`.
wlr-output-management reports the state per head and turns it on or off as the
rule would. Tested against fake KMS and the headless backend; nothing here has
run on a VRR display.

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
second deadline.

A Wayland drag over an X window is bridged through Xdnd (#310): the drag hands
the window to the X side instead of offering it as a Wayland data device, and
the window manager owns `XdndSelection` with the drag's types, sends
`XdndEnter`, `XdndPosition` and `XdndLeave` (through a verified `XdndProxy`
when the window names one), and passes `XdndStatus` back as the source's
target and action. A drop the X window did not accept, or one on a window
without `XdndAware`, cancels the drag; an accepted one sends `XdndDrop`, serves
the conversions from the Wayland source as a selection is served, and finishes
the source on `XdndFinished`, or cancels it when that does not come within the
transfer timeout.

An X drag reaches Wayland windows through a drag proxy: an input-only,
`XdndAware` window over the whole root. While an X client owns `XdndSelection`,
the compositor reports the surface under the pointer on every grabbed motion,
and the window manager maps the proxy on top whenever that surface is not an
X one, so the X source finds it and sends its Xdnd messages there. The
window manager turns them into a Wayland drag whose source is the X
selection: `XdndEnter` starts it with the X types as mime types, each
`XdndPosition` moves it to the surface under the pointer and is answered with
the Wayland target's accept and action, `XdndLeave` cancels it, and
`XdndDrop` drops it; the Wayland target reads the data by converting
`XdndSelection`, and its finish, or the drop's refusal, is sent back as
`XdndFinished`. The pointer stays with Xwayland during the drag, so the X
source keeps its grab. `nix build .#checks.x86_64-linux.vm-xdnd` drags files
between dragon-drop running on Wayland and on X, both ways.

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
| InputCapture | lys-shell's `lys-shell-lys-wm.portal`, over the `capture` IPC ([`docs/ipc.md`](ipc.md#input-capture)) |
| everything else | lys-shell, then xdg-desktop-portal-gtk |

lys-shell's portal is the shell the session runs anyway. It implements every
portal interface without depending on a compositor: it captures through
ext-image-copy-capture or wlr-screencopy (#53), names windows by their
ext-foreign-toplevel identifier and outputs by connector name, and drives
RemoteDesktop through the virtual keyboard and pointer protocols (#52).
xdg-desktop-portal-wlr speaks wlr-screencopy, so screen sharing still works
without lys-shell. GlobalShortcuts needs `hyprland-global-shortcuts-v1` in the
compositor (#89). InputCapture needs lys-wm's `capture` IPC, so lys-shell declares it in
`lys-shell-lys-wm.portal`, a second portal file that only this config routes
to: on any other desktop the frontend never exports it.

lys-wm reads one setting back from the portal: `org.freedesktop.appearance
accent-color`, the `accent` palette colour the focus border and active tab
default to. lys-ui's `portalsettings` monitor follows it on its own goroutine,
and a change posts a config reload to the loop (stubbedev/lys-settings#4).

Quickshell captures single windows only through
`hyprland_toplevel_export_manager_v1` (#312), keyed by a wlr foreign-toplevel
handle or, in `capture_toplevel`, by the lys-wm window ID. Its frames share
wlr-screencopy's frame code: shm or dmabuf, the window drawn without
decorations or rounding as ext-image-copy-capture draws it, also while it is
hidden, and a copy that waits for damage waits for a commit in the window.

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
| `securitycontext` | `wp_security_context_manager_v1`: Flatpak and other sandbox engines register a socket for their apps; the compositor restricts screen capture, data control, foreign toplevels, output management, virtual input, session lock, layer shell, workspaces, global shortcuts and client hotkeys, gamma and colour-matrix control and input methods to unsandboxed clients (#118, #123, #124, #176) |
| `wlclient` | The client side of a connection: object map, zombies and dispatch, for the nested backend and tests (#6, #9) |
| `wlclient/feedback` | Reads `zwp_linux_dmabuf_feedback_v1` as a client: the device a compositor renders with and each tranche's format and modifier pairs |
| `backend` | The backend interface: outputs, frames, input devices (#8) |
| `backend/headless` | Virtual outputs and injected input, for tests (#8) |
| `backend/nested` | Outputs as windows on a parent Wayland compositor (#9) |
| `backend/drm` | Atomic KMS on one DRM device (#40) |
| `render` | The renderer interface (#7) |
| `render/soft` | The CPU renderer, for headless and golden tests (#7) |
| `render/gles` | The GLES2 renderer (#34) |
| `render/vulkan` | The Vulkan renderer, on lys-lib's `gpu` package, with the same capabilities as GLES (#155) |
| `render/rendertest` | The scene and checks every renderer is held to the software renderer's pixels with |
| `gpu` | purego bindings for EGL, GLES2 and GBM (#33) |
| `surface` | `wl_compositor`, `wl_surface`, `wl_region`, `wl_subcompositor`, `wp_viewporter`, `wp_fractional_scale_v1`, `wp_presentation`: double-buffered state, a queue of content updates that apply in order once their gates open, subsurface trees, roles (#11, #19, #117) |
| `tearingcontrol` | `wp_tearing_control_manager_v1`: the vsync or async presentation hint, which travels with its content update; with `tearing` on, a fullscreen direct scanout of a surface that asks, that a rule allows, or that hints nothing and is a game, goes out as an async page flip of the primary plane's `FB_ID` alone (#122) |
| `contenttype` | `wp_content_type_manager_v1`: a surface's none, photo, video or game tag, latched with its content update; IPC reports it per window, and a game counts as asking to tear when it gives no tearing hint (#153) |
| `fifo` | `wp_fifo_manager_v1`: a barrier an update sets when it applies and the next latching deadline clears, which a later update can wait for (#117) |
| `committiming` | `wp_commit_timing_manager_v1`: an update waits until the next refresh of an output showing it would not present it before its target time (#117) |
| `gammacontrol` | `zwlr_gamma_control_manager_v1`: one client per output sets its gamma ramps, which the drm backend tests and writes to the CRTC's `GAMMA_LUT`; the identity returns when the client lets go (#123) |
| `colorrep` | `wp_color_representation_manager_v1`: a client states a YCbCr surface's coefficients (BT.601, BT.709, BT.2020), range and chroma siting; the compositor imports its dmabufs with the matching EGL hints instead of guessing BT.709 from 720 lines up and BT.601 below. Only premultiplied alpha is advertised, and coefficients or siting on an RGB buffer are a pixel_format error at commit (#189) |
| `colorspace` | Transfer functions, primaries, ICC profiles and the transform between any two encodings; pure |
| `colormanager` | `wp_color_manager_v1`, version 3: parametric and matrix-shaper ICC descriptions, surface tags latched with their content update, output descriptions and preferred-description feedback. Perceptual intent only |
| `ctmcontrol` | `hyprland_ctm_control_manager_v1`: the first manager bound sets outputs' 3×3 colour transform matrices on commit, as hyprsunset does, written as S31.32 to the CRTC's `CTM`, which sits before `GAMMA_LUT` so both apply; later managers get `blocked`, and every output it changed returns to the identity when it goes. An output whose CRTC has no `CTM` keeps its colours and the request is reported (#176) |
| `alphamodifier` | `wp_alpha_modifier_v1`: clients fade a surface by an opacity factor the compositor applies when it draws it |
| `hyprlandsurface` | `hyprland_surface_manager_v1`, version 2: a surface's overall opacity, multiplied with its alpha modifier for the content and the blur behind it, and its visible region, buffer rects the surface is drawn as pieces of, so what it hides is neither drawn, damaged nor counted as covering; an empty region draws the whole surface, as Hyprland does. Both latch with the content update (#314) |
| `backgroundeffect` | `ext_background_effect_v1`: clients name the region of a surface to blur behind, latched with its content update; the compositor draws it through the same blur as `decoration.blur` |
| `dmabuf` | `zwp_linux_dmabuf_v1`: GPU buffers shared as dmabuf planes, checked against what the renderer imports, and the feedback that names the formats, modifiers and device |
| `syncobj` | `wp_linux_drm_syncobj_manager_v1`: explicit synchronization through DRM timeline syncobjs |
| `idle` | `ext_idle_notifier_v1` and `zwp_idle_inhibit_manager_v1`: idle timers that input resets, and inhibitors that hold them off while their surface is on screen |
| `outputpower` | `zwlr_output_power_manager_v1`: clients such as idle daemons turn outputs off and on |
| `singlepixel` | `wp_single_pixel_buffer_manager_v1`: one-pixel buffers of a solid color (#20) |
| `region` | Pixel regions as disjoint rectangles, for damage and input and opaque regions |
| `output` | `wl_output` and `zxdg_output_v1`: each output's mode, logical position and scale in 120ths, with rounding that keeps logical edges adjacent in device pixels (#19) |
| `outputmanagement` | `zwlr_output_manager_v1`: outputs as heads with their modes for display configuration tools, whose configurations override the config file's output rules until a reload (#107) |
| `shm` | `wl_shm` pools and buffers, read fault-safely when a client truncates its pool (#12) |
| `inputmethod` | `zwp_text_input_manager_v3`, `zwp_text_input_manager_v1` (Chromium and Electron default to it; a v1 text input enters when it activates on the keyboard-focused surface, and its `commit_state` serial tags what it receives, #309) and `zwp_input_method_manager_v2`: text inputs follow the keyboard focus, the enabled one activates the seat's single input method, and its text, preedit and deletions reach that text input; the input method's keyboard grab takes the keys pressed while it holds it, and its popups are drawn under the text cursor (#124) |
| `seat` | `wl_seat`, keyboard, pointer and touch, `wp_cursor_shape_v1`, and touchpad gestures through `zwp_pointer_gestures_v1`, which stay with the surface they began on unless the compositor claims them, when the client gets a cancelled end (#13, #14, #108); drawing tablets through `zwp_tablet_manager_v2`: a tool follows the pointer onto the surface under it, stays on the surface its tip went down on, and clicks as a pointer for a client without the protocol; a pad is announced with its mode groups, buttons, rings, strips and dials, follows the keyboard focus, enters with the tablet in its libinput device group and reports a group's mode switches before the event that made them; Xwayland binds the same protocol, so X apps get pressure as an XI2 valuator (#308) |
| `data` | `wl_data_device_manager` and `zwp_primary_selection_device_manager_v1`: the clipboard and primary selection offered to the keyboard focus, and drag and drop (#18) |
| `lys-lib/xcursor` | Xcursor files and theme lookup through `XCURSOR_PATH` and `Inherits`, and the built-in arrow, shared with lys-ui (#14) |
| `xkb` | Keymap compilation, sealed keymap fds and modifier state through lys-lib's `xkb` (#13) |
| `shell` | xdg-shell and layer-shell glue between clients and `wm`: toplevels, popups placed by their positioner, explicit popup grabs, server-side decorations (xdg-decoration, and KDE's server-decoration for GTK3 clients, both always server-side; #180), activation tokens, and xdg-foreign handles that make another client's toplevel, such as a portal dialog, a child of the exported window, and xdg-dialog's modal hint; focus that would land on a window with a mapped modal dialog goes to the dialog, for X windows with `_NET_WM_STATE_MODAL` too (#15, #16, #17, #20, #48, #110, #111); `xdg_toplevel_icon_v1` icons, copied out of their shm buffers when assigned and latched with the toplevel's commit, for IPC and the tab bar (#154); `xdg_toplevel_tag_v1` tags, which window rules match, and descriptions (#315) |
| `wm` | The pure core and the dispatcher (#21, #66) |
| `anim` | Easing curves, springs, tweens and the per-event animation table (#223) |
| `binds` | Typed chords (modifiers with a keycode from the first layout, a button or a scroll direction), submaps and bind flags, matched to `wm` actions (#29) |
| `xwayland` | Runs Xwayland for X11 clients: the display sockets, the start on the first connection, the X11 window manager and the selection bridge (#44, #45, #46); `_NET_WM_STATE_DEMANDS_ATTENTION` marks a window urgent until it is focused, a maximize request is answered with the unmaximized state and the current geometry, `_NET_CLIENT_LIST_STACKING` follows the raises, and `_NET_WORKAREA` is the focused output's usable area, below its exclusive layer surfaces, in Xwayland's coordinates (#311) |
| `config` | The typed config, its validation and reload (#30) |
| `ipc` | The IPC socket and event stream (#31) |
| `systemd` | Hands the session's environment to D-Bus activation and the systemd user manager, and runs `lys-wm-session.target` while lys-wm runs |
| `session` | logind, udev and libinput: device access, hotplug and VT switching (#38, #39, #42); the `handle-lid-switch` inhibitor, logind's Suspend, and the power supplies' AC state for the lid policy (#179) |
| `powerprofile` | power-profiles-daemon's `ActiveProfile` on the system bus, followed off the loop, for the power saver frame cap (#384) |
| `tabletinput` | Drawing tablets, their tools' proximity, motion, tip and button events, and their pads' layout and button, ring, strip and dial events, from libinput to the compositor (#308) |
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

## Escape beside a dropdown

`lys_escape_v1`, a private protocol pinned from lys-lib, lets a client claim the
next unmodified Escape press without taking the keyboard, because no standard
protocol takes one key from the focused window and taking the whole keyboard
traps the user. A key goes to the first of these that wants it: a compositor
bind (a client cannot shadow a user's `Escape`, such as `submap reset`); the
lock surface; a popup grab, exclusive layer, input method grab or drag; the
focused surface when it inhibits shortcuts, which asked for every key; the
armed client's `wl_keyboard` when it holds the focus, so one press never
arrives twice; the newest arm; the focused surface.

An arm is one-shot, so a client that stopped reading costs one key press. It
dies with its client, and sandboxed clients cannot bind it. A taken press stays
in `held` until its release, so disarming mid-press leaks nothing to the window,
and a repeated press is swallowed without a second event. A key the window
already saw go down is never taken, as its release is owed. Virtual keyboards
take part, with the keysym read from their own keymap, compiled on the first
press an arm could take.

## Virtual touch

`lys_virtual_touch_v1`, a private protocol pinned from lys-lib, gives a remote
desktop (lys-shell's RemoteDesktop portal, for `NotifyTouch*` and an EIS
client's `ei_touchscreen`) a touchscreen spanning the output layout, since no
standard protocol injects touch. A position is a fraction of the layout's
bounding box, as `zwlr_virtual_pointer_v1.motion_absolute` places the pointer,
and the points go through the same path as a real touchscreen's. Each device
numbers its points in a block of ids of its own, above the ids touchscreens
use, so two never collide. A device adds the touch capability to the seat while
it exists; destroying it, or its client going, cancels the points it left down.
Sandboxed clients cannot bind it, and an upgrade in place drops its points.

## Blocking surfaces out of captures

A window or layer rule's `block_out_from` keeps a surface out of screen
sharing. Each capture is classified at the protocol boundary and fails closed:
it is a screencast unless its client declared it a one-shot screenshot through
`lys_capture_hint_v1`, a private protocol pinned from lys-lib that sandboxed
clients cannot bind, before its first frame. Painting a capture replays the
output's last frame with each blocked surface's ops replaced by an opaque fill
of the same shape, one for one, so it costs no extra pass and the screen is
untouched; a capture of a blocked window itself is a black rectangle. While
the blocked rects stay where they were, their damage is left out of a
screencast's, so content changes behind the mask wake no cast frame.

## Focus grabs

`hyprland_focus_grab_v1` (#313) works as an xdg popup grab does, over the
committed whitelist of surfaces instead of one client's popups: only
whitelisted surfaces (and their subsurfaces) get pointer focus, the keyboard
stays on a whitelisted surface or moves to the newest one, and a press or touch
outside clears the grab, sends `cleared` and goes nowhere else. A new grab
clears the one before it, and locking the session clears it.

## Client hotkeys

`xx_hotkey_v1` (#327) is offered only with `client_hotkeys: true`, since the
protocol is experimental. A key trigger matches the keysym a key produces at
the first level of any layout, with exactly its Shift, Ctrl, Alt and Super
held, so it keeps working after a layout switch. Hotkeys come after
compositor binds and before everything else: a fired press joins `held` like a
bind's, so its release is swallowed too, and a button joins `consumed`.
Triggers are exclusive: a commit that a compositor bind or another hotkey
already uses is denied `already_bound`, and a reload whose binds take a bound
trigger revokes it. A lone modifier is a tap: it fires on release when nothing
else was pressed, scrolled or clicked meanwhile, and is never consumed. The
policy is the spec's suggested default. A handover revokes every hotkey, as
replaying a hotkey's requests would rerun the policy against a config the
client never saw; the client decides whether to bind again.

## Protocol bindings

`internal/protocol/protocols.json` pins every protocol source and maps each
XML file to a package. `go run ./internal/scanner -fetch` refreshes the
vendored XML from it; `go generate ./internal/scanner` regenerates both
sides, and a test fails when the committed tree is stale.

These XML files are not shared with lys-lib's `wlr`, though 30 of them are
the same bytes. lys-wm's are the newest of each protocol at a pinned source and
`-fetch` reproduces them, because the server has to advertise those versions;
`wlr`'s are the client API lys-ui and lys-shell build against, and change when
a client needs them to. Sharing would make every server protocol bump a lys-lib
bump and a client API change first.

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
instance. The primary render device is an explicit choice passed to whatever
needs it, never an implicit global. [GPU](#gpu) has the selection rule (#62).

## Testing

- Unit tests for the pure core and every package with logic of its own.
- Golden images, byte-exact, from the software renderer.
- The compositor-in-the-loop harness (#10) boots the same compositor
  `cmd/lys-wm` runs on the headless backend, in process, with a private
  `XDG_RUNTIME_DIR`, and drives real clients from the dev shell against it.
  `just test` runs it with `LYS_WM_HEADLESS=1`, which turns a missing
  client into a failure; it is part of `just gates`, so CI runs it too.

## Power saver

While power-profiles-daemon reports `power-saver` (#384), every output above
`power_saver_frame_rate` paces its frames on its k-th vblank, k the refresh over
the cap rounded up. The cap is a pacing interval the backends use wherever a
frame aims at a vblank: the frame clock's latch and the render budget, the
aimed vblank a frame is timed against, idle refreshes and the refresh in
presentation feedback, so animations sample the vblank they land on and take
as long. The cursor plane keeps the mode's own latch, and under adaptive sync
the cap is a minimum frame interval. `internal/powerprofile` follows
`ActiveProfile` on the system bus off the loop (the UPower name, then the
legacy `net.hadess` one) and posts changes; a daemon that is absent or leaves
means no cap, and nothing polls.

## The lid

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
suspend once (#205). The power read and the Suspend call run off the loop, one policy check at a time.
A Suspend logind refuses is reported, never fatal.
A power supply that disappears while it is read, as USB-C supplies do on
plug and unplug, is skipped; any other read error is reported and leaves the
power state unknown, so the policy does not suspend (#211).
After a wake the policy runs again once logind's default lid holdoff (30 s)
has passed, so a laptop woken with the lid still closed, undocked and on
battery sleeps again, while a lid opened to wake it, or a dock that comes up,
is seen first; a suspend that fails at once cannot spin (#212).

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
- KDE's `server-decoration` protocol XML is LGPL-2.1-or-later. It is vendored
  unchanged with its licence header and only describes the wire interface,
  as wlroots, sway and Hyprland ship it; no KDE code is ported.

## Upgrading a running session

`lys-wmctl upgrade <path>` moves a session started with `--holder` to another
lys-wm binary without logging out (#254). The switch is off by default, and
without it nothing below runs: one process, logind in-process, no journal. It
stays off until the path has run on hardware other than a VM's. Two decisions
carry it.

**The session runs as a holder and a worker.** With `--holder`, the `lys-wm`
process a display manager starts stays small and never changes: it holds the logind session and
the devices it took, forwards signals, and exits with its worker's status. The
compositor is a child of it, and asks it for devices over a socket
(`internal/session/remote.go`). logind ties a session's devices to one D-Bus
connection, so the process that holds that connection cannot be replaced; a
worker can. The holder speaks a small versioned protocol, and a worker that
cannot speak it refuses the upgrade.

**The state between binaries is Wayland requests.** A dump of Go structs would
tie a new binary to the old one's internals, and every field added later would
be a chance to forget one. Instead the old compositor writes, for each client,
the requests that rebuild its objects, and the new one runs them through its
ordinary handlers while nothing reaches the client. The protocol is the only
schema two binaries are certain to share, and the new binary's state is what
its own handlers made of them.

- `server/journal.go` keeps what each object needs, by `server/policy.go`: the
  request that created it, the latest of each setter, every `wl_region` edit.
  `server/testdata/handover-policy.txt` records the reviewed class of every
  request of every protocol, and a test fails when a new one has none.
- Surfaces and buffers are written from their state (`surface/handover.go`),
  since a surface's history is unbounded and only its result matters.
- What cannot be rebuilt is retired with a legal event: popups are dismissed,
  foreign toplevel and output heads close and come back under new ids, clipboard
  sources are cancelled, captures fail, focus leaves. The client sees what any
  compositor may do at any time, and its stale objects stay inert until it
  destroys them.
- Colour management moves like the rest: an image description is the
  requests that made it, replayed, and an ICC profile is replayed from the
  bytes the old compositor read (at most `maxICCSize`), through a memfd, so the
  client's file may have changed or gone since. Descriptions get the new
  compositor's identities, so once the clients are released every
  `wp_color_management_output_v1` hears `image_description_changed` and every
  surface feedback `preferred_changed`, and clients query again. A surface's
  tag is journaled as its latest `set_image_description` and latches with the
  surface state `surface/handover.go` writes. Description information is a
  one-shot stream: one still open is retired with its `done`.
- What cannot be retired refuses the upgrade before anything changes, and the
  refusal names the client and the reason: a session lock, a sandbox listener,
  an exported window. Xwayland is refused too unless
  `--drop-x11` says to end it, as its window manager's X connection and every X
  client's state have no equivalent of the above.

The old compositor stays alive and parked, not reading its clients, until the
new one has rebuilt every client, restored the layout, presented a frame on
every lit output and said it is ready. Only then does it commit and exit. A new
binary that fails, crashes or hangs at any point before that leaves the old one
to resume with nothing changed, as the new one has sent nothing to any client.
Both share the one DRM file description the holder took, so master never moves;
the parked one does not read its flip events, and frees its framebuffers as it
leaves.

Clients keep their connection because the file descriptors are passed; the
Wayland and IPC listening sockets, and the X sockets, move the same way, so
`WAYLAND_DISPLAY` and `DISPLAY` stay valid.
