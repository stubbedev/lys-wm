# IPC

lys-wm listens on `$XDG_RUNTIME_DIR/lys-wm/<WAYLAND_DISPLAY>.sock` and exports
the path as `LYS_WM_SOCKET` to everything it starts. `lys-wmctl` finds it
through `--socket`, then `LYS_WM_SOCKET`, then the only socket in
`$XDG_RUNTIME_DIR/lys-wm`.

## lys-wmctl

```sh
lys-wmctl dispatch workspace 3     # any action from actions.md
lys-wmctl reload                   # the same as: lys-wmctl dispatch reload
lys-wmctl outputs | workspaces | windows | activewindow | version
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
`activewindow`, `dispatch` (which needs `action`), `icon` (which needs
`window` and `size`) and `watch`. Unknown
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

`workspaces`: a list of open workspaces, in ID order:

```json
{"id":1,"output":"DP-1","windows":3,"visible":true,"focused":true}
```

`windows`: a list of mapped windows, oldest first. `activewindow` is the
focused one, or `null`:

```json
{"id":7,"app_id":"foot","title":"~","pid":4242,"workspace":1,"output":"DP-1",
 "rect":{"x":0,"y":0,"width":1024,"height":1152},"floating":false,"fullscreen":false,"urgent":false,"unresponsive":false,"focused":true,
 "content_type":"none","icon":{"serial":3,"name":"foot","sizes":[32,64]}}
```

Window IDs are never reused while lys-wm runs. For an X11 window, `app_id` is
the class half of `WM_CLASS`, `title` is `_NET_WM_NAME` or else `WM_NAME`, and
`pid` is `_NET_WM_PID`, or 0 when the client does not set it.
`content_type` is what the window's surface says it shows through
`wp_content_type_v1`: `none`, `photo`, `video` or `game`.

`icon` is what the window set through `xdg_toplevel_icon_v1`, or `null` when
it set none and the app ID's own icon applies. `name` is an icon theme name
and `sizes` lists the edges, in pixels, of the square buffers it gave; either
may be missing. `serial` changes whenever the icon does, so a client can cache
icons by it. X11 windows have no icon here yet.

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
shows, the focused output, windows, focus, submap and layout.

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
| `icon` | `window`, `icon` | A window set or cleared its icon |
| `urgent` | `window`, `urgent` | A window asked for attention, or got it |
| `unresponsive` | `window`, `unresponsive` | A window's client stopped answering pings, or answered again. lys-wm pings a window's client when the window gains focus (xdg clients, and X clients that list `_NET_WM_PING`) and marks it after 5 seconds without an answer |
| `focus` | `window` (ID or `null`) | Keyboard focus moved to another window, or to none |
| `submap` | `submap` (`""` for the default binds) | The bind submap changed |
| `layout` | `layout` (`index`, `name`) | The keyboard layout group changed |

A watcher that falls 256 events behind is disconnected. Reconnect and
re-query to resynchronise.
