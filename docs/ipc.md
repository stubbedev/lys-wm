# IPC

lys-wm listens on `$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock` and exports
the path as `LYS_WM_SOCKET` to everything it starts.

## lys-wmctl

`lys-wmctl` finds the socket through `--socket <path>`, then `LYS_WM_SOCKET`,
then the only socket in `$XDG_RUNTIME_DIR/lys-wm`.

| Command | Does |
| --- | --- |
| `dispatch <action> [args...]` | Run an [action](./actions) |
| `batch <action>...` | Run each argument as an action, in order, as one [batch](#batches) |
| `reload` | Same as `dispatch reload`; prints what the config did not apply as warnings |
| `upgrade [--drop-x11] <path>` | Move the session to another lys-wm binary; see [Upgrading in place](./guide/upgrade) |
| `outputs`, `workspaces`, `windows`, `activewindow`, `submap`, `layout`, `locks`, `reducedmotion`, `overview`, `scratchpads`, `configproblems`, `layers`, `devices`, `binds`, `casttarget`, `version` | Print that [query](#results) as JSON |
| `pickwindow` | Wait for a click and print the window's ID, or `null`; Escape cancels |
| `watch [--snapshot]` | Print [events](#events) as JSON lines until lys-wm exits |
| `icon <window> <size>` | Print the window's icon buffer closest to `size` pixels |
| `output create [--name <name>] [--scale <s>] <w>x<h>[@<hz>]` | Add a [virtual output](#virtual-outputs), 60 Hz by default; prints `{"name":...}` |
| `output remove <name>` | Remove a virtual output |

Exit status: 0 on success, 3 when no lys-wm answers on the socket, 1 for any
other failure (the error goes to stderr).

## Protocol

One request per connection: the client sends one JSON line, reads one
response line, and the server closes the connection (except `watch` and
`capture`). Requests must arrive within 5 seconds and be under 64 KiB.

```json
{"request":"dispatch","action":"movetoworkspace 3"}
{"version":1}
```

| `request` | Fields |
| --- | --- |
| `dispatch` | `action` (a string) or `actions` (a list), exactly one |
| `icon` | `window` (ID), `size` (pixels, above 0) |
| `upgrade` | `binary` (absolute path), optional `drop_x11` |
| `createoutput` | `mode` (`width`, `height`, `refresh_mhz`), optional `name`, `scale` |
| `removeoutput` | `name` |
| `watch` | optional `snapshot` |
| `capture` | none; see [Input capture](#input-capture) |
| every query | none |

Unknown requests and unknown or misplaced fields are errors. The response
always has `version` (1 until the protocol changes incompatibly), then either
`error` or, where there is one, `result`. A client should refuse a version it
does not know.

### Results

**`outputs`**: a list of

```json
{"name":"DP-1","description":"LG Electronics 27GL850","rect":{"x":0,"y":0,"width":2048,"height":1152},
 "scale":1.25,"mode":{"width":2560,"height":1440,"refresh_mhz":143912},"workspace":1,"focused":true,"powered":true}
```

| Field | Meaning |
| --- | --- |
| `rect` | Logical pixels, after the transform |
| `workspace` | The workspace shown; absent on a mirror |
| `powered` | False while `dpms off` or an idle daemon has the output off |
| `shader`, `shader_error` | Path of the [screen shader](./shaders); why it is off, if it is |
| `hdr` | Present and true while the output carries an HDR signal |
| `zoom` | The zoom factor; absent at 1 |
| `transform` | `90`, `flipped-270`, ...; absent when normal |
| `mirror_of` | The output this one [mirrors](./config#outputs) |

**`workspaces`**: open workspaces in ID order;
`name` only when a rule names it.

```json
{"id":1,"output":"DP-1","windows":3,"visible":true,"focused":true}
```

**`scratchpads`**: the [scratchpads](./config#scratchpads) that hold a window or
are shown; `output` is where each shows or last showed.

```json
{"name":"term","output":"DP-1","shown":true,"windows":1}
```

**`windows`**: mapped windows, oldest first. **`activewindow`**: the focused
one, or `null`.

```json
{"id":7,"app_id":"foot","title":"~","pid":4242,"workspace":1,"output":"DP-1",
 "rect":{"x":0,"y":0,"width":1024,"height":1152},"floating":false,"fullscreen":false,"maximized":false,
 "client_fullscreen":false,"pinned":false,"minimized":false,"urgent":false,"unresponsive":false,
 "focused":true,"inhibits_shortcuts":false,"content_type":"none","content_source":"client",
 "icon":{"serial":3,"name":"foot","sizes":[32,64]},"stack":0,"focus_timestamp":12,
 "group":{"id":4294967297,"layout":"h","index":0}}
```

| Field | Meaning |
| --- | --- |
| `id` | Never reused while lys-wm runs |
| `app_id`, `title`, `pid` | For X11: the class half of `WM_CLASS`, `_NET_WM_NAME` or `WM_NAME`, `_NET_WM_PID` (0 if unset) |
| `workspace`, `scratchpad` | A window in a scratchpad has `workspace` 0 and `scratchpad` set to its name |
| `rect` | Layout coordinates; for a minimized window, where it returns when floating |
| `maximized` | Fills the usable area |
| `client_fullscreen` | The client is told it is fullscreen, including fake fullscreen in its tile |
| `pinned` | Stays on screen across its output's workspace switches; `workspace` is the one shown |
| `minimized` | Hidden, still counted in its workspace |
| `unresponsive` | The client has not answered a ping for 5 seconds; pings go out when the window gains focus |
| `inhibits_shortcuts` | Holds an active `zwp_keyboard_shortcuts_inhibitor_v1`: only `locked` and `inhibit_proof` binds run |
| `content_type` | `none`, `photo`, `video` or `game` |
| `content_source` | `rule` (a rule's `content` key) or `client` (`wp_content_type_v1`) |
| `icon` | From `xdg_toplevel_icon_v1`, or `null`. `name`: theme icon name; `sizes`: edges of the kept square buffers (up to 256 px, or the smallest if all are larger); `serial` changes with the icon. X11 windows have none |
| `stack` | Front-to-back index on its output, 0 on top (fullscreen, maximized, floating, tiled); absent when not shown |
| `swallowed_by` | ID of the window that [swallowed](./config#swallow) this terminal; absent otherwise |
| `focus_timestamp` | Sequence number, bumped when focus settles on the window; sort descending for MRU order. 0 = never focused |
| `group` | The container of a tiled window, `null` when floating. `id` is opaque and stable for the container's life; `layout` is `h`, `v` or `tab`; `index` is the position among its children (the tab in a tab group) |

**`layers`**: mapped layer-shell surfaces, in creation order. `id` is never
reused; `rect` is in layout coordinates.

```json
{"id":3,"namespace":"waybar","layer":"top","output":"DP-1","rect":{"x":0,"y":0,"width":2048,"height":30},"keyboard_interactivity":"none"}
```

**`devices`**: input devices. `capabilities` from `keyboard`, `pointer`,
`touch`, `tablet`, `tablet_pad`; keyboards also carry the seat's shared
`layout`.

```json
{"name":"AT Translated Set 2 keyboard","capabilities":["keyboard"],"layout":{"index":0,"name":"English (US)"}}
```

**`binds`**: every bind of the loaded config, in config order. `chord` is as
written; `flags` lists any of `locked`, `repeating`, `release`, `pass`,
`inhibit_proof` (a lone modifier always has `release`); `long_press_ms` and
`cooldown_ms` only when set.

```json
{"submap":"","chord":"SUPER","action":"global shell:launcher","description":"Open the launcher","flags":["release"],"cooldown_ms":150}
```

**State queries**, each matching an [event](#events) of the same name:

| Query | Result |
| --- | --- |
| `submap` | Active submap name, `""` for the default binds |
| `layout` | `{"index":0,"name":"English (US)"}`: layout group index and keymap description |
| `locks` | `{"caps":true,"num":false,"scroll":false}`, from the keymap's LEDs |
| `overview` | Whether the [overview](./config#overview) has input |
| `reducedmotion` | Whether `animations.reduced_motion` is in force |
| `configproblems` | `{"rejected":false,"messages":[]}`: `rejected` when the whole file was refused and the previous config runs; `messages` lists what was not applied |

**`casttarget`**: what the dynamic screencast source shows:
`{"kind":"window","window":7}`, `{"kind":"output","output":"DP-1"}` or
`{"kind":"none"}`. The `casttarget` action sets it. A capture client creates
the source with `lys_dynamic_capture_source_manager_v1.create_source`
(lys-lib's `lys-dynamic-capture-v1.xml`, a privileged global) and opens an
ext-image-copy-capture session on it. The session waits for the first target
and follows every change without stopping: new buffer constraints only on a
size change, full damage on the first frame after a change. A window target
follows its window anywhere; when the window closes or the output goes, the
target clears and frames are black at the last size.

**`icon`**: the window's icon buffer with the smallest edge at or above
`size`, else the largest: `{"size":64,"scale":1,"png":"<base64>"}`. An error
when the window has no icon buffer; resolve a named icon through the icon
theme yourself.

**`pickwindow`**: waits for the next click and answers the window ID under it,
or `null`. The cursor is a crosshair and no client sees the pointer or click.
Escape cancels with an error; so does closing the connection. One pick at a
time; the client waits up to 2 minutes.

**`upgrade`**: `{"pid":...,"binary":...,"clients":...,"windows":...}` for the
new compositor process.

**`version`**: `1`.

### Batches

`dispatch` with `actions` runs them in order in one pass of the event loop, so
they land as one transaction: watchers see one burst, each window's `rect`
once. It stops at the first failure with `action <index> (<text>): ...`
(index from 0); earlier actions stay applied.

```json
{"request":"dispatch","actions":["workspace 2","exec foot","togglefloating"]}
```

### Virtual outputs

`createoutput` adds an output that exists only in memory, e.g. to stream to
another device:

```json
{"request":"createoutput","mode":{"width":1920,"height":1080,"refresh_mhz":60000},"scale":1.5,"name":"tablet"}
{"version":1,"result":{"name":"tablet"}}
```

- `mode` is required and is the only mode: every field above 0, edges up to
  16384, refresh up to 1000 Hz.
- `name` defaults to the lowest free `VIRT-<n>` and must be unused.
- `scale` overrides the [output rules](./config#outputs); without it the rules
  decide, as they do position and `disabled`.
- It behaves like a monitor in `outputs`, `wl_output`, `xdg_output`,
  wlr-output-management and ext-workspace, and gets workspaces.
- It renders offscreen on the session's GPU at its refresh rate, only while a
  capture (ext-image-copy-capture, wlr-screencopy, the screencast portal) is
  on it. Otherwise its windows get frame callbacks at
  [`hidden_frame_rate`](./config#hidden_frame_rate).

`removeoutput` removes a virtual output by `name`: its workspaces and windows
move as when a monitor is unplugged, and its captures stop. Virtual outputs
last until removed or until lys-wm exits, and block an
[upgrade](./guide/upgrade). For permanent ones, use
[`virtual_outputs`](./config#virtual_outputs).

## Debugging a running session

The `debug` action works on a live session:

```sh
lys-wmctl dispatch debug damage on      # tint each frame's damage, fading over 8 frames
lys-wmctl dispatch debug opaque on      # tint what the culler treats as opaque
lys-wmctl dispatch debug planes on      # tint composited frames; untinted = direct scanout or overlay plane
lys-wmctl dispatch debug trace start /tmp/frames.jsonl
lys-wmctl dispatch debug trace stop     # same format as -frame-trace; see Frame pacing
lys-wmctl dispatch debug profile start /tmp/cpu.pprof
lys-wmctl dispatch debug profile stop
lys-wmctl dispatch debug heap /tmp/heap.pprof
lys-wmctl dispatch debug protocol on    # keep each client's recent Wayland messages in memory
lys-wmctl dispatch debug protocol dump /tmp/wire.log
lys-wmctl dispatch debug protocol off   # drop them
```

- Overlays take `on`, `off` or `toggle` (default). They are drawn after the
  scene and are not scene damage, so an idle screen stops redrawing once the
  last tint fades. `debug planes` never takes a window off direct scanout.
- Paths must be absolute. Only one trace and one profile at a time; a trace
  started with `-frame-trace` cannot be stopped here.
- `debug protocol on` records the last 4096 messages each client sent and was
  sent, so a client that stops responding can be looked at after the fact
  without having run it under `WAYLAND_DEBUG`. A message keeps the first 128
  bytes of its arguments; anything longer shows as `<truncated>`. Recording
  costs nothing while off, and a client that disconnects takes its record with
  it, as does an [upgrade](./guide/upgrade).
- `debug protocol dump` writes every connected client's record, oldest first,
  in the `WAYLAND_DEBUG` format. Each line is tagged `{pid/connection}`, since
  one process can hold several connections with their own object IDs, and its
  time counts in milliseconds from the start given on the first line.
  `LYS_WM_DEBUG=wire` prints the same lines live to stderr.

The trace format is described in [Frame pacing](./frame-pacing#the-trace).

## Events

`watch` answers `{"version":1}`, then writes one JSON object per line, each
with an `event` field. With `"snapshot":true` it first writes the current
state as the events that would build it from nothing (outputs, workspaces,
windows, focus, scratchpads, layers, binds, submap, layout, locks, ...). The
snapshot and the live stream are cut in one loop pass, so nothing falls
between them.

Events from one loop pass arrive in this order: `dispatch`; per output
`output` and `workspace`; `focusedoutput`; per window `close`, `open`, then
its changes; `focus`; `showdesktop`; `mru`; `switcher`; `binds`; `submap`;
`layout`; `locks`; `reducedmotion`; `overview`; `casttarget`;
`configproblems`; `scratchpad`; `layer`.

| `event` | Fields | When |
| --- | --- | --- |
| `dispatch` | `action` | An action ran, from a bind or IPC |
| `output` | `change` (`added`, `removed`, `changed`), `output` | An output appeared, went, or changed geometry, scale, mode, description or power |
| `workspace` | `output`, `workspace` | An output shows another workspace |
| `focusedoutput` | `output` | Focus moved to another output |
| `open` | `window` (full object) | A window mapped |
| `close` | `window` (ID) | A window unmapped |
| `move` | `window`, `workspace` | A window moved to another workspace |
| `title` | `window`, `title` | Title changed |
| `appid` | `window`, `app_id` | App ID changed |
| `rect` | `window`, `output`, `rect` | Moved, resized or changed output |
| `icon` | `window`, `icon` | Icon set or cleared |
| `urgent` | `window`, `urgent` | Asked for attention, or got it |
| `mode` | `window`, `floating`, `fullscreen`, `maximized`, `client_fullscreen` | Any of those changed |
| `minimized` | `window`, `minimized` | Minimized or restored |
| `pin` | `window`, `pinned` | Pinned or unpinned |
| `swallow` | `window`, `swallowed_by` | Swallowed (ID) or shown again (0) |
| `unresponsive` | `window`, `unresponsive` | Stopped or resumed answering pings |
| `stack` | `window`, `stack` (index or `null`) | Front-to-back index changed, or shown/hidden |
| `group` | `window`, `group` (or `null`) | Entered, left or moved within a container |
| `focus` | `window` (ID or `null`) | Keyboard focus moved |
| `showdesktop` | `output`, `on` | `showdesktop` slid an output's windows aside, or back |
| `mru` | `windows` (IDs, most recent first) | MRU order changed |
| `switcher` | `open`, `selected` (ID or `null`), `title` | Switcher opened, changed selection or closed; `title` is for screen readers |
| `binds` | `binds` (as the `binds` query) | A reload changed the binds |
| `submap` | `submap` | Submap changed |
| `layout` | `layout` (`index`, `name`) | Keyboard layout group changed |
| `locks` | `locks` (`caps`, `num`, `scroll`) | A lock key toggled |
| `reducedmotion` | `reduced_motion` | A reload toggled `animations.reduced_motion` |
| `overview` | `open` | Overview opened (true as soon as it takes input) or started closing |
| `casttarget` | `target` | Dynamic screencast target changed |
| `configproblems` | `rejected`, `messages` | A config load left something out, was refused, or a later one fixed it |
| `scratchpad` | `name`, `output`, `shown`, `windows` | A scratchpad appeared, showed, hid, moved or changed count; only `name` when it went away |
| `layer` | `change` (`open`, `close`), `id`, `namespace`, `output`, `layer` | Layer surface mapped or went |
| `bell` | `window` (ID or `null`) | `xdg_system_bell_v1` rang |
| `windowmenu` | `window`, `x`, `y` | `xdg_toplevel.show_window_menu`; the point is in layout coordinates |

A watcher 256 events behind is disconnected; reconnect and re-query.

## Input capture

`capture` lets a software KVM (Deskflow, Input Leap, lan-mouse, via
lys-shell's InputCapture portal) take the local pointer and keyboard when the
pointer crosses a screen edge. It answers `{"version":1}` and stays open both
ways: the client writes commands, lys-wm writes events, as JSON lines. The
first event is `keymap`. Closing the connection ends the capture.

| `command` | Fields | Effect |
| --- | --- | --- |
| `barriers` | `barriers`: list of `{"id","x1","y1","x2","y2"}` | Replace the client's barriers and disable it; an active capture ends with `deactivated` |
| `enable` | | Arm the barriers |
| `disable` | | Disarm them; an active capture ends without an event |
| `release` | optional `at` (`{"x","y"}`) | End the active capture, warping the pointer to `at` (clamped to the outputs); answered by `deactivated` |

A barrier is a horizontal (`y1 == y2`) or vertical (`x1 == x2`) line in
logical pixels, with a non-zero `id` unique to the client. It lies on the top
or left edge of the pixels it names, both ends inclusive: the right edge of a
1920x1080 output at 0,0 is `{"x1":1920,"y1":0,"x2":1920,"y2":1079}`. It
triggers when relative motion would cross it, even though the pointer stops
at the edge. Barriers do not trigger while the session is locked, a pointer
constraint is active, a button is held on a surface or a drag is running.
Only one capture is active at a time.

Each applied command is answered by `{"event":"applied"}` after the events it
caused. A malformed or refused command gets `{"event":"error","error":"..."}`
and ends the capture.

| `event` | Fields | When |
| --- | --- | --- |
| `keymap` | `keymap` (xkb text) | Capture opened, or a reload changed the keymap |
| `activated` | `activation`, `at` (`x`, `y`), `barrier` | Pointer pushed through `barrier`; `at` is where it would have gone; `activation` counts up per capture |
| `deactivated` | `activation`, `at` | Ended by `release`, new `barriers` or lys-wm; `at` is the pointer |
| `disabled` | | lys-wm ended the capture and disabled the client (session lock, or Super+Escape); send `enable` to re-arm |
| `motion` | `dx`, `dy` | Relative motion, accelerated |
| `button` | `button` (evdev code), `pressed` | |
| `scroll` | `dx`, `dy` | Smooth scroll, logical pixels |
| `scroll_discrete` | `dx`, `dy` | Wheel, in 120ths of a detent |
| `scroll_stop` | `x`, `y` | Smooth scroll ended on those axes |
| `frame` | | Ends a group of pointer events |
| `key` | `key` (evdev code), `pressed` | |
| `modifiers` | `depressed`, `latched`, `locked`, `group` | At activation and whenever a key changes them |

While a capture is active, no input reaches surfaces and no binds run: the
focused surface gets `wl_keyboard.leave` and `wl_pointer.leave`, and the
pointer stays put. When it ends, keyboard focus returns with held keys and the
pointer re-enters whatever is under it. Super+Escape always ends the capture
and disables the client; no client sees the press.
