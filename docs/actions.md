# Actions

Every keybind, every `gestures` entry with an `action` and `lys-wmctl dispatch` takes one of these. A gesture can also drive something directly: see `gestures` in config.md. This file is generated from `wm.Actions`; run `go test ./internal/wm -update` after changing them.

| Action | Arguments | Effect |
| --- | --- | --- |
| `focus` | `<left\|right\|up\|down>` | Move focus to the neighbouring window, or to the next output at the edge. |
| `move` | `<left\|right\|up\|down>` | Move the focused window, hy3 style; at the workspace edge nothing happens. |
| `workspace` | `<n\|e+n\|e-n\|previous>` | Show workspace n, the n-th open workspace after or before the current one, or the one the output showed before it. |
| `movetoworkspace` | `<n\|e+n\|e-n\|previous>` | Move the focused window to a workspace without following it. |
| `kill` |  | Ask the focused window to close. |
| `focuswindow` | `<id>` | Focus the window with this IPC ID, showing its workspace on its output and raising it, and its tab when grouped; a minimized window comes back. |
| `minimize` | `[id]` | Hide the focused window, or the one with this IPC ID, with its dialogs; the rest of its workspace takes its space. |
| `restore` | `[id]` | Bring back the window with this IPC ID, or the one minimized last on the focused workspace, and focus it. |
| `overview` | `[toggle\|show\|hide]` | Open or close the overview on every output: each output's windows side by side under a strip of its workspaces. Without an argument, toggle. |
| `closewindow` | `<id>` | Ask the window with this IPC ID to close. |
| `dpms` | `<on\|off\|toggle> [output]` | Turn outputs on or off, or one output by name; off outputs keep their windows and wake only on dpms on. |
| `reload` |  | Read the config file again; a bad file keeps the running config and reports the error. |
| `dismissconfignotice` |  | Hide the notice on screen about config problems; `lys-wmctl configproblems` still lists them, and the next reload shows what it finds. |
| `exit` |  | End the session: lys-wm closes its clients, Xwayland and IPC socket and exits, as on SIGTERM. |
| `nextlayout` |  | Switch the keyboard to its next layout, wrapping after the last. With one layout it does nothing. |
| `global` | `<app_id>:<id>` | Press the global shortcut an application registered, and release it with the bind. |
| `exec` | `<command>` | Run a shell command with sh -c. |
| `makegroup` | `<h\|v\|tab> [ephemeral]` | Wrap the focused window in a new group; an ephemeral group dissolves when one window is left. |
| `submap` | `<name\|reset>` | Switch binds to the named submap, or back to the default ones. |
| `togglefloating` |  | Float the focused window centred at half the output, or tile it again. |
| `fullscreen` |  | Make the focused window cover its output, above the top layer, or end that. |
| `resizeactive` | `<dx> <dy>` | Grow or shrink the focused window by pixels; tiled windows move their right or bottom edge, else their left or top one. |
| `movewindow` |  | Mouse binds only: drag the window under the pointer; tiled windows drop into the tree where released. A window drawing its own title bar starts the same drag with xdg_toplevel.move or _NET_WM_MOVERESIZE, with the button or touch point it was pressed with. |
| `resizewindow` |  | Mouse binds only: resize the window under the pointer from its nearest corner; tiled windows change their split ratios. A window drawing its own edges starts the same drag from the edge it names with xdg_toplevel.resize or _NET_WM_MOVERESIZE. |
| `zoom` | `<in\|out\|reset\|set> [factor]` | Magnify the output under the pointer around the pointer: by factor, 1.5 unless given, from where it is going, back to 1, or to factor, up to 16. |
