# IPC

lys-wm listens on `$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock` and exports
the path as `LYS_WM_SOCKET` to everything it starts. `lys-wmctl` finds it
through `--socket`, then `LYS_WM_SOCKET`, then the only socket in
`$XDG_RUNTIME_DIR/lys-wm`.

## lys-wmctl

```sh
lys-wmctl dispatch workspace 3     # any action from actions.md
lys-wmctl batch "workspace 2" "exec foot" # several actions, in order and at once
lys-wmctl reload                   # the same as: lys-wmctl dispatch reload
lys-wmctl upgrade <path>           # move the session to the lys-wm at path; see Upgrading in place
lys-wmctl outputs | workspaces | windows | activewindow | submap | layout | locks | reducedmotion | overview | scratchpads | configproblems | layers | devices | binds | version
lys-wmctl watch [--snapshot]       # events as JSON lines, until lys-wm exits; the state first with --snapshot
lys-wmctl pickwindow               # click a window to print its ID; Escape cancels
lys-wmctl icon 7 64                # window 7's icon buffer closest to 64 pixels
lys-wmctl output create 1920x1080@60 # add a virtual output; prints {"name":"VIRT-1"}
lys-wmctl output remove VIRT-1      # remove it again
```

Queries print JSON. A failed request prints the error and exits 1.

## Protocol

A connection sends one request as a JSON line and reads one response line,
then the server closes it:

```json
{"request":"dispatch","action":"movetoworkspace 3"}
{"version":1}
```

`request` is one of `version`, `outputs`, `workspaces`, `windows`,
`activewindow`, `submap`, `layout`, `locks`, `reducedmotion`, `overview`, `scratchpads`, `configproblems`, `layers`, `devices`, `binds`, `pickwindow`, `dispatch` (which needs `action` or `actions`, see [Batches](#batches)), `icon` (which needs
`window` and `size`), `upgrade` (which needs `binary`, and takes `drop_x11`; see
[Upgrading in place](./guide/upgrade)), `createoutput` (which needs `mode`, and
takes `name` and `scale`), `removeoutput` (which needs `name`), `watch` (which takes `snapshot`, see [Events](#events)) and `capture`
(see [Input capture](#input-capture)). Unknown
requests and fields are errors. The response always has `version`, which is
1 until the protocol changes incompatibly; it then has either `error` or, for
queries, `result`. A client should refuse to talk to a version it does not
know.

Requests must arrive within 5 seconds and be under 64 KiB.

### Results

`outputs`: a list of

```json
{"name":"DP-1","description":"LG Electronics 27GL850","rect":{"x":0,"y":0,"width":2048,"height":1152},
 "scale":1.25,"mode":{"width":2560,"height":1440,"refresh_mhz":143912},"workspace":1,"focused":true,"powered":true}
```

`rect` is in logical pixels; `workspace` is the one the output shows;
`powered` is false while `dpms off` or an idle daemon has the output off.
An output with a [screen shader](./shaders) also has `shader`, its path, and,
when it stays off, `shader_error`, which says why. An output carrying an HDR
signal has `hdr: true`, so a config that asked for it can see that it got it. A zoomed output has `zoom`, the factor it is going to, or a pinch's once it ends; at 1 the field is left out.

`workspaces`: a list of open workspaces, in ID order:

```json
{"id":1,"output":"DP-1","windows":3,"visible":true,"focused":true}
```

A workspace a rule names also has `name`.

`scratchpads`: the [scratchpads](./config#scratchpads) that exist, which are the
ones holding a window and the ones shown; `output` is the one each shows over
or last showed over:

```json
{"name":"term","output":"DP-1","shown":true,"windows":1}
```

`windows`: a list of mapped windows, oldest first. `activewindow` is the
focused one, or `null`:

```json
{"id":7,"app_id":"foot","title":"~","pid":4242,"workspace":1,"output":"DP-1",
 "rect":{"x":0,"y":0,"width":1024,"height":1152},"floating":false,"fullscreen":false,"maximized":false,"client_fullscreen":false,"pinned":false,"minimized":false,"urgent":false,"unresponsive":false,"focused":true,"inhibits_shortcuts":false,
 "content_type":"none","content_source":"client","icon":{"serial":3,"name":"foot","sizes":[32,64]},"stack":0,
 "group":{"id":4294967297,"layout":"h","index":0}}
```

`group` is the container a tiled window sits in, or `null` for a floating one:
`{"id":4294967298,"layout":"tab","index":1}`. `id` is opaque and stays the
same while the container exists, however its members change; `layout` is `h`,
`v` or `tab`; `index` is the window's place among the container's children, 0
first, which in a tab group is its tab.

`stack` is the window's front-to-back index among the windows its output
shows, 0 on top: fullscreen windows, then maximized, then floating, then tiled, each in the
order they are drawn. It is left out for a window no output shows, on a hidden
workspace or minimized. A client can tell which window is on top at a point
from one `windows` request.

Window IDs are never reused while lys-wm runs. For an X11 window, `app_id` is
the class half of `WM_CLASS`, `title` is `_NET_WM_NAME` or else `WM_NAME`, and
`pid` is `_NET_WM_PID`, or 0 when the client does not set it.
`pinned` is true while the window stays on screen through workspace switches of its output (the `pin` action); its `workspace` is the one its output shows.

`swallowed_by` is the ID of the window that took this terminal's place in the layout when it was started from it (see `swallow` in config.md); it is left out while the terminal shows.

`focus_timestamp` counts up each time focus settles on a window (see `switcher` in config.md): ordering windows by it, highest first, gives the most-recently-used order; 0 means the window has not had focus yet. It is a sequence number, not a time.

A window in a scratchpad has `workspace` 0 and `scratchpad`, its name; other
windows leave `scratchpad` out.
`maximized` is true while the window fills the usable area (the `maximize`
action or its own request); `client_fullscreen` while it is told it is
fullscreen, which a window under `fullscreen on fake` is while it stays in its
tile.
`inhibits_shortcuts` is true while the window holds a
`zwp_keyboard_shortcuts_inhibitor_v1` that is active, so only `locked` and `inhibit_proof` binds
run while it has the keyboard (a shortcut recorder, a virtual machine).
A minimized window stays on its workspace, and in the workspace's `windows`
count, with `minimized` true and the `rect` it comes back to when floating.
`content_type` is what lys-wm takes the window to show: `none`, `photo`,
`video` or `game`. `content_source` says where that came from: `rule` when a
rule's `content` key sets it, otherwise `client`, what its surface says through
`wp_content_type_v1`. It decides `vrr: fullscreen`, the connector's content type
and whether a window that gives no tearing hint tears.

`icon` is what the window set through `xdg_toplevel_icon_v1`, or `null` when
it set none and the app ID's own icon applies. `name` is an icon theme name
and `sizes` lists the edges, in pixels, of the square buffers it gave that
lys-wm keeps: those up to 256 pixels, or only the smallest when every buffer
is larger. Either may be missing. `serial` changes whenever the icon does, so a client can cache
icons by it. X11 windows have no icon here yet.

`submap`: the active bind submap's name, `""` for the default binds.

`layout`: the active keyboard layout group, as
`{"index":0,"name":"English (US)"}`. `index` counts the configured layouts
from 0; `name` is the layout's description from the keymap.

`locks`: the lock keys' state as the keymap's LEDs show it,
`{"caps":true,"num":false,"scroll":false}`.

`overview`: whether the [overview](./config#overview) has the input, `true` or
`false`.

`reducedmotion`: whether `animations.reduced_motion` is in force, `true` or
`false`. lys-shell follows it, and serves it to apps as the portal's
`org.freedesktop.appearance` `reduced-motion`.

`configproblems`: what the last load of the config did not apply, the same text as the
notice on screen. `rejected` is true when the whole file was refused and the
previous config is still running; `false` with `messages` means the file
applied without the keys named. `{"rejected":false,"messages":[]}` when clean.

These six answer at any time what the `submap`, `layout`, `locks`, `overview`, `reducedmotion` and `configproblems`
events report when they change, so a client that starts later knows them at
once.

`icon`: the window's icon buffer whose edge is the smallest at or above
`size` pixels, or else the largest, as
`{"size":64,"scale":1,"png":"<base64>"}`. It is an error when the window has no
icon buffer; an icon given only by name is resolved through the icon theme by
the client.

`layers`: the mapped layer-shell surfaces (bars, docks, wallpapers, launchers),
in the order they were created:

```json
{"id":3,"namespace":"waybar","layer":"top","output":"DP-1","rect":{"x":0,"y":0,"width":2048,"height":30},"keyboard_interactivity":"none"}
```

`id` is never reused while lys-wm runs; `layer` is `background`, `bottom`,
`top` or `overlay`; `rect` is in layout coordinates; `keyboard_interactivity`
is `none`, `exclusive` or `on_demand`.

`devices`: the input devices, as
`{"name":"AT Translated Set 2 keyboard","capabilities":["keyboard"],"layout":{"index":0,"name":"English (US)"}}`.
`capabilities` lists `keyboard`, `pointer`, `touch`, `tablet` and
`tablet_pad`, as the device has them; a keyboard also has `layout`, the
seat's active layout group, which every keyboard shares.

`binds`: every bind of the loaded config, in config order, as
`{"submap":"","chord":"SUPER","action":"global shell:launcher","description":"Open the launcher","flags":["release"],"cooldown_ms":150}`.
`chord` is as written in the config; `flags` lists `locked`, `repeating`,
`release`, `pass` and `inhibit_proof` as set (a lone modifier always has
`release`); `long_press_ms` and `cooldown_ms` appear when set.

`pickwindow`: waits for the next click and answers with the ID of the window
under it, or `null` over no window (a bar, the wallpaper). The cursor is a
crosshair meanwhile, and no client sees the pointer or the click. Escape
cancels with an error. Only one pick runs at a time; closing the connection
cancels it. A client waits up to 2 minutes.

`version`: the number 1.

### Batches

`dispatch` with `actions`, a list of action strings, runs them in order in one
pass of lys-wm's loop, so they land in one transaction: a watcher sees one
burst of events, each window's `rect` once. It stops at the first action that
fails and answers with an error naming its index from 0 and its text,
`action 1 (kill): ...`; the actions before it stay applied. A request has
either `action` or `actions`.

```json
{"request":"dispatch","actions":["workspace 2","exec foot","togglefloating"]}
```

### Virtual outputs

`createoutput` adds an output that exists only in memory, for a stream to
another device or a screencast at a size of its own:

```json
{"request":"createoutput","mode":{"width":1920,"height":1080,"refresh_mhz":60000},"scale":1.5,"name":"tablet"}
{"version":1,"result":{"name":"tablet"}}
```

`mode` is required, every field above 0, the edges up to 16384 pixels and
the rate up to 1000 Hz; it is the output's only mode.
`name` defaults to the lowest free `VIRT-<n>` and must not be taken by
another output. Without `scale`, the [output rules](./config#outputs) decide
it as for any output; a given `scale` beats the rules.
The output is placed, gets workspaces, and shows in `outputs`, `wl_output`,
`xdg_output`, wlr-output-management and ext-workspace like a monitor, and the
output rules (position, scale, disabled) match its name.

It renders offscreen on the GPU that renders the session, paced by its own
clock at the mode's refresh rate, and only while a capture
(ext-image-copy-capture, wlr-screencopy, the screencast portal) has a
session or a frame on it. The rest of the time it renders nothing, and its
windows get their frame callbacks at
[`hidden_frame_rate`](./config#hidden_frame_rate), so an output
nobody watches costs no GPU work.

`removeoutput` takes it away again, by `name`, and answers no result: its
workspaces and windows move to the remaining outputs, as when a monitor is
unplugged, and its captures stop. Only virtual outputs can be removed. They
last until removed or until lys-wm exits, and an [upgrade](./guide/upgrade)
is refused while any exists. For outputs that last, list them under
[`virtual_outputs`](./config#virtual_outputs) in the config.

## Debugging a running session

The `debug` action answers on the session that misbehaves, without a restart:

```sh
lys-wmctl dispatch debug damage on      # tint each frame's damage, fading over 8 frames
lys-wmctl dispatch debug opaque on      # tint what the culler takes as opaque
lys-wmctl dispatch debug planes on      # tint composited frames; untinted means direct scanout or an overlay plane
lys-wmctl dispatch debug trace start /tmp/frames.jsonl
lys-wmctl dispatch debug trace stop     # the file is what -frame-trace writes; lys-wm-pace reads it
lys-wmctl dispatch debug profile start /tmp/cpu.pprof
lys-wmctl dispatch debug profile stop
lys-wmctl dispatch debug heap /tmp/heap.pprof
```

The overlays take `on`, `off` or `toggle` (the default) and are drawn after
the scene, so they never count as the scene's own damage: a still screen
under `debug damage on` stops drawing once the last tint has faded. With every
overlay off, a frame costs what it did before. `debug planes` tints only
frames lys-wm composites, so it never takes a window off direct scanout.
Files are absolute paths. A trace started with `-frame-trace` cannot be
stopped here, and a second trace or profile is refused while one records;
the trace is written by its own goroutine, as with the flag, and the heap
profile off the loop.

## Events

`watch` answers `{"version":1}`, then writes one JSON object per line. Each
has an `event` field and the fields below. With `"snapshot":true` it first
writes the current state as events: an `output` (`added`) and a `workspace`
for each output, `focusedoutput`, an `open` for each window, `focus`, the
scratchpads, a `layer` (`open`) for each layer surface, the binds and the submap,
layout and locks, then the live events. Both are taken in one pass of the
loop, so a window that maps meanwhile is in one or the other, never in
neither. Events that happen together
arrive in this order: dispatches, then each output with the workspace it
shows, the focused output, windows, focus, submap, layout, locks and reduced motion.

| `event` | Fields | When |
| --- | --- | --- |
| `dispatch` | `action` | An action ran, from a bind or from IPC |
| `output` | `change` (`added`, `removed`, `changed`), `output` | An output appeared, went away, or changed its geometry, scale, mode, description or power |
| `workspace` | `output`, `workspace` | An output shows another workspace |
| `focusedoutput` | `output` | Focus moved to another output |
| `open` | `window` (the full window) | A window mapped |
| `close` | `window` (its ID) | A window unmapped |
| `move` | `window`, `workspace` | A window moved to another workspace |
| `title` | `window`, `title` | A window changed its title |
| `appid` | `window`, `app_id` | A window changed its app ID |
| `rect` | `window`, `output`, `rect` | A window moved, resized or went to another output; `rect` is as in `windows` |
| `icon` | `window`, `icon` | A window set or cleared its icon |
| `urgent` | `window`, `urgent` | A window asked for attention, or got it |
| `mode` | `window`, `floating`, `fullscreen`, `maximized`, `client_fullscreen` | A window floated or tiled, went fullscreen or maximized, or back, or was told it is fullscreen (fake fullscreen), or no longer |
| `group` | `window`, `group` (as in `windows`, or `null`) | A window entered, left or moved within a container: tiled, floated, grouped with `makegroup`, or a sibling came or went before it |
| `layer` | `change` (`open`, `close`), `id`, `namespace`, `output`, `layer` | A layer surface mapped or went away; the fields are as in `layers` |
| `bell` | `window` (ID or `null`) | A client rang the system bell (`xdg_system_bell_v1`), for its window or for none |
| `pin` | `window`, `pinned` | A window was pinned to its output (the `pin` action) or unpinned |
| `swallow` | `window`, `swallowed_by` | A terminal was swallowed by the window started from it (`swallowed_by` its ID) or came back (`swallowed_by` 0) |
| `minimized` | `window`, `minimized` | A window was minimized, or came back |
| `unresponsive` | `window`, `unresponsive` | A window's client stopped answering pings, or answered again. lys-wm pings a window's client when the window gains focus (xdg clients, and X clients that list `_NET_WM_PING`) and marks it after 5 seconds without an answer |
| `stack` | `window`, `stack` (index or `null`) | A window's front-to-back index on its output changed, or it was shown or hidden |
| `focus` | `window` (ID or `null`) | Keyboard focus moved to another window, or to none |
| `submap` | `submap` (`""` for the default binds) | The bind submap changed |
| `binds` | `binds` (as in the `binds` query) | A reload changed the binds |
| `mru` | `windows` (IDs, most recently used first) | Focus settled on a window, or one in the order closed |
| `switcher` | `open`, `selected` (ID or `null`), `title` | The switcher opened, moved its selection or closed; `title` is the selected window's, for a screen reader to announce |
| `layout` | `layout` (`index`, `name`) | The keyboard layout group changed |
| `locks` | `locks` (`caps`, `num`, `scroll`) | Caps, Num or Scroll Lock turned on or off |
| `reducedmotion` | `reduced_motion` | A reload turned `animations.reduced_motion` on or off |
| `configproblems` | `rejected`, `messages` | A load of the config left something out or was refused, or a later one fixed it |
| `overview` | `open` | The overview opened or started closing. `open` is true from the moment it takes input, before its animation ends |
| `scratchpad` | `name`, `output`, `shown`, `windows` | A scratchpad appeared, showed, hid, moved or changed its window count; one that went away comes with only its `name` |
| `showdesktop` | `output`, `on` | The `showdesktop` action slid an output's windows aside, or they came back |
| `windowmenu` | `window`, `x`, `y` | A window asked for its window menu (`xdg_toplevel.show_window_menu`) in answer to a press; `x`, `y` are the point in layout coordinates, for a shell to draw the menu at |

A watcher that falls 256 events behind is disconnected. Reconnect and
re-query to resynchronise.

## Input capture

`capture` is how a software KVM (Deskflow, Input Leap, lan-mouse, through
lys-shell's InputCapture portal) takes the local pointer and keyboard when the
pointer crosses a screen edge. It answers `{"version":1}` and then stays open
in both directions: the client writes commands as JSON lines, and lys-wm
writes events as JSON lines, each with an `event` field. The first event is
`keymap`. Closing the connection ends the capture.

Commands:

| `command` | Fields | Effect |
| --- | --- | --- |
| `barriers` | `barriers`: a list of `{"id","x1","y1","x2","y2"}` | Replaces the client's barriers and disables it; an active capture of the client ends with `deactivated` |
| `enable` | | Arms the barriers |
| `disable` | | Disarms them; an active capture of the client ends without an event |
| `release` | `at` (optional `{"x","y"}`) | Ends the client's active capture, warping the pointer to `at` (clamped to the outputs) first; answered by `deactivated` |

A barrier is a horizontal (`y1 == y2`) or vertical (`x1 == x2`) line in
logical pixels with a non-zero `id` unique among the client's barriers, on the
top or left edge of the pixels it names, both ends inclusive: the right edge
of a 1920x1080 output at 0,0 is `{"x1":1920,"y1":0,"x2":1920,"y2":1079}`. A
barrier triggers when relative pointer motion would cross its line, even
though the pointer itself stops at the edge. Barriers do not trigger while the
session is locked, a pointer constraint is active, a button is held on a
surface or a drag is in progress, and only one capture is active at a time.

Every command that applies is answered by `{"event":"applied"}`, after the
events it caused. A malformed or refused command is answered by
`{"event":"error","error":"..."}` and ends the capture.

Events:

| `event` | Fields | When |
| --- | --- | --- |
| `keymap` | `keymap` (xkb keymap text) | The capture opened, or a reload changed the keymap |
| `activated` | `activation`, `at` (`x`, `y`), `barrier` | The pointer pushed through `barrier`; `at` is where the motion would have put it, past the edge. `activation` grows by one with each capture |
| `deactivated` | `activation`, `at` | The capture ended by `release`, by new `barriers`, or by lys-wm; `at` is the pointer |
| `disabled` | | lys-wm ended the capture and disabled the client: a session lock, or Super+Escape. The client sends `enable` to arm its barriers again |
| `motion` | `dx`, `dy` | Relative pointer motion, accelerated |
| `button` | `button` (evdev code), `pressed` | |
| `scroll` | `dx`, `dy` | Smooth scrolling in logical pixels |
| `scroll_discrete` | `dx`, `dy` | Wheel scrolling in 120ths of a detent |
| `scroll_stop` | `x`, `y` | Smooth scrolling on those axes ended |
| `frame` | | Ends a group of pointer events |
| `key` | `key` (evdev code), `pressed` | |
| `modifiers` | `depressed`, `latched`, `locked`, `group` | The keyboard's modifier state, at activation and whenever a key changes it |

While a capture is active, lys-wm routes no pointer or keyboard input to
surfaces and runs no binds: the focused surface gets `wl_keyboard.leave` and
`wl_pointer.leave`, and the pointer stays where it stopped. When the capture
ends, the keyboard focus comes back with the keys still held, and the pointer
re-enters whatever is under it. Super+Escape always ends the capture and
disables the client; the press reaches no client.
