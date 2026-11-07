# IPC

lys-wm listens on `$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock` and exports
the path as `LYS_WM_SOCKET` to everything it starts. `lys-wmctl` finds it
through `--socket`, then `LYS_WM_SOCKET`, then the only socket in
`$XDG_RUNTIME_DIR/lys-wm`.

## lys-wmctl

```sh
lys-wmctl dispatch workspace 3     # any action from actions.md
lys-wmctl reload                   # the same as: lys-wmctl dispatch reload
lys-wmctl upgrade <path>           # move the session to the lys-wm at path; see Upgrading in place
lys-wmctl outputs | workspaces | windows | activewindow | submap | layout | locks | reducedmotion | overview | configproblems | version
lys-wmctl watch                    # events as JSON lines, until lys-wm exits
lys-wmctl icon 7 64                # window 7's icon buffer closest to 64 pixels
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
`activewindow`, `submap`, `layout`, `locks`, `reducedmotion`, `overview`, `configproblems`, `dispatch` (which needs `action`), `icon` (which needs
`window` and `size`), `upgrade` (which needs `binary`, and takes `drop_x11`; see
[Upgrading in place](./guide/upgrade)) and `watch`. Unknown
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

`windows`: a list of mapped windows, oldest first. `activewindow` is the
focused one, or `null`:

```json
{"id":7,"app_id":"foot","title":"~","pid":4242,"workspace":1,"output":"DP-1",
 "rect":{"x":0,"y":0,"width":1024,"height":1152},"floating":false,"fullscreen":false,"maximized":false,"client_fullscreen":false,"minimized":false,"urgent":false,"unresponsive":false,"focused":true,
 "content_type":"none","content_source":"client","icon":{"serial":3,"name":"foot","sizes":[32,64]},"stack":0}
```

`stack` is the window's front-to-back index among the windows its output
shows, 0 on top: fullscreen windows, then maximized, then floating, then tiled, each in the
order they are drawn. It is left out for a window no output shows, on a hidden
workspace or minimized. A client can tell which window is on top at a point
from one `windows` request.

Window IDs are never reused while lys-wm runs. For an X11 window, `app_id` is
the class half of `WM_CLASS`, `title` is `_NET_WM_NAME` or else `WM_NAME`, and
`pid` is `_NET_WM_PID`, or 0 when the client does not set it.
`maximized` is true while the window fills the usable area (the `maximize`
action or its own request); `client_fullscreen` while it is told it is
fullscreen, which a window under `fullscreen on fake` is while it stays in its
tile.
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

`version`: the number 1.

## Events

`watch` answers `{"version":1}`, then writes one JSON object per line. Each
has an `event` field and the fields below. Events that happen together
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
| `mode` | `window`, `fullscreen`, `maximized`, `client_fullscreen` | A window went fullscreen or maximized, or back, or was told it is fullscreen (fake fullscreen), or no longer |
| `minimized` | `window`, `minimized` | A window was minimized, or came back |
| `unresponsive` | `window`, `unresponsive` | A window's client stopped answering pings, or answered again. lys-wm pings a window's client when the window gains focus (xdg clients, and X clients that list `_NET_WM_PING`) and marks it after 5 seconds without an answer |
| `stack` | `window`, `stack` (index or `null`) | A window's front-to-back index on its output changed, or it was shown or hidden |
| `focus` | `window` (ID or `null`) | Keyboard focus moved to another window, or to none |
| `submap` | `submap` (`""` for the default binds) | The bind submap changed |
| `layout` | `layout` (`index`, `name`) | The keyboard layout group changed |
| `locks` | `locks` (`caps`, `num`, `scroll`) | Caps, Num or Scroll Lock turned on or off |
| `reducedmotion` | `reduced_motion` | A reload turned `animations.reduced_motion` on or off |
| `configproblems` | `rejected`, `messages` | A load of the config left something out or was refused, or a later one fixed it |
| `overview` | `open` | The overview opened or started closing. `open` is true from the moment it takes input, before its animation ends |
| `windowmenu` | `window`, `x`, `y` | A window asked for its window menu (`xdg_toplevel.show_window_menu`) in answer to a press; `x`, `y` are the point in layout coordinates, for a shell to draw the menu at |

A watcher that falls 256 events behind is disconnected. Reconnect and
re-query to resynchronise.
