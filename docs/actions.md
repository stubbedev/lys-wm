# Actions

Every keybind, every `gestures` entry with an `action` and `lys-wmctl dispatch` takes one of these. A gesture can also drive something directly: see `gestures` in config.md. This file is generated from `wm.Actions`; run `go test ./internal/wm -update` after changing them.

| Action | Arguments | Effect |
| --- | --- | --- |
| `focus` | `<left\|right\|up\|down>` | Move focus to the neighbouring window, or to the next output at the edge. |
| `move` | `<left\|right\|up\|down>` | Move the focused window, hy3 style; at the workspace edge it crosses to the neighbouring output's workspace, entering its tree at the near edge, and focus follows. A floating window steps by `floating.step` pixels inside the usable area, and from its edge crosses to the neighbouring output. |
| `workspace` | `<n\|name\|e+n\|e-n\|m+n\|m-n\|empty\|previous>` | Show workspace n, or the one a workspace rule names, on its bound output when a rule binds it; the n-th open workspace after or before the current one, of every output (e) or of the focused output (m); the lowest free number, opened on the focused output (empty); or the one the output showed before it. |
| `movetoworkspace` | `<n\|name\|e+n\|e-n\|m+n\|m-n\|empty\|previous> [follow]` | Move the focused window to a workspace, chosen as for workspace; with follow, show that workspace with the window focused, in one switch. |
| `focusoutput` | `<left\|right\|up\|down\|name>` | Focus the neighbouring output in that direction, or the output with that name, and the window focused there. |
| `movetooutput` | `<left\|right\|up\|down\|name>` | Move the focused window onto the workspace that output shows and focus it there; a floating window keeps its place relative to the output. |
| `moveworkspacetooutput` | `<left\|right\|up\|down\|name>` | Move the focused workspace with its windows, layout and focus to that output and show it there; floating windows keep their place relative to the output, inside its area. |
| `swapworkspaces` | `<output> <output>` | Swap the workspaces two outputs show, each with its windows; an output is a direction from the focused one or a name. |
| `scratchpad` | `<toggle <name>\|hide>` | Show the named scratchpad over the focused output, above its workspace and dimming it, or hide it when it shows there; one shown elsewhere moves here. A scratchpad holds windows like a workspace that no number reaches. Toggling one that is empty runs its `exec` from `scratchpads`, and the window that opens lands in it. hide hides the one over the focused output. |
| `movetoscratchpad` | `<name>` | Move the focused window into the named scratchpad; it hides with the scratchpad unless that shows on its output. movetoworkspace brings it back. |
| `kill` |  | Ask the focused window to close. |
| `focuswindow` | `<id>` | Focus the window with this IPC ID, showing its workspace on its output and raising it, and its tab when grouped; a minimized window comes back. |
| `minimize` | `[id]` | Hide the focused window, or the one with this IPC ID, with its dialogs; the rest of its workspace takes its space. |
| `restore` | `[id]` | Bring back the window with this IPC ID, or the one minimized last on the focused workspace, and focus it. |
| `overview` | `[toggle\|show\|hide] [workspace\|output\|app]` | Open or close the overview on every output, under a strip of each output's workspaces: the windows of the workspace shown (workspace, the default), every window of the output grouped by workspace (output), or the focused window's app across every workspace and output, on the focused output (app). Without a mode, toggle. |
| `closewindow` | `<id>` | Ask the window with this IPC ID to close. |
| `dpms` | `<on\|off\|toggle> [output]` | Turn outputs on or off, or one output by name; off outputs keep their windows and wake only on dpms on. |
| `reload` |  | Read the config file again; a bad file keeps the running config and reports the error. |
| `dismissconfignotice` |  | Hide the notice on screen about config problems; `lys-wmctl configproblems` still lists them, and the next reload shows what it finds. |
| `exit` |  | End the session: lys-wm closes its clients, Xwayland and IPC socket and exits, as on SIGTERM. |
| `nextlayout` |  | Switch the keyboard to its next layout, wrapping after the last. With one layout it does nothing. |
| `stickykeys` | `<on\|off>` | Turn sticky keys on or off: a modifier pressed and released alone applies to the next key, pressed twice it stays on until pressed again. |
| `slowkeys` | `<ms>` | Accept a key only once it was held this many milliseconds; 0 turns slow keys off. |
| `global` | `<app_id>:<id>` | Press the global shortcut an application registered, and release it with the bind. |
| `exec` | `<command>` | Run a shell command with sh -c. |
| `makegroup` | `<h\|v\|tab> [ephemeral]` | Wrap the focused window in a new group; an ephemeral group dissolves when one window is left. |
| `setlayout` | `<tree\|scroll>` | Turn the focused workspace into a tree of splits and tab groups, or into a scrollable strip of columns: each top-level child becomes a column, keeping its share of the width, and nested groups flatten into the column's stack; back, the columns become a horizontal split. Every window and the focus stay. |
| `columnwidth` | `<preset next\|preset prev\|+n%\|-n%\|n%>` | On a scrolling workspace, set the focused column's width to the next or previous of `scroll.presets`, change it by n percent of the output's width, or set it to n percent. |
| `consume` | `<left\|right>` | On a scrolling workspace, move the focused window into the bottom of the column on that side. |
| `expel` |  | On a scrolling workspace, take the focused window out of its column into a column of its own to the right. |
| `center` |  | On a scrolling workspace, scroll the view so the focused column is in the middle of the output. |
| `columndisplay` | `<stack\|tab>` | On a scrolling workspace, show the windows of the focused column stacked over each other or as tabs. |
| `changegroup` | `<h\|v\|tab\|toggletab\|opposite>` | Change the layout of the focused group, or of the group around the focused window, keeping its ratios. toggletab switches between tabs and the split it had before, h for a group made as tabs; opposite turns h into v and back. |
| `focustab` | `<prev\|next\|n> [wrap]` | Show the previous, next or n-th tab of the nearest tab group around the focused window; prev and next stop at the ends unless wrap. |
| `focusparent` |  | Focus the group around the focused window or group, drawn with the active border, so move, swap, moveout, kill, togglefloating and movetoworkspace act on all of it; keyboard input still goes to the window inside. |
| `focuschild` |  | Undo one focusparent, down toward the focused window. |
| `swap` | `<left\|right\|up\|down>` | Exchange the focused window or group with its neighbour in that direction; each slot keeps its size. |
| `moveout` |  | Lift the focused window or group out of its group, placing it after that group in the one around it. |
| `equalize` | `[all]` | Give the children of the focused group, or of the group around the focused window, equal shares again; all does so for the whole tree. |
| `locktab` | `[toggle\|on\|off]` | Lock the nearest tab group around the focused window: new windows open beside it instead of in it, and move and swap neither enter nor leave it, though its tabs still reorder. toggle unless told. |
| `submap` | `<name\|reset>` | Switch binds to the named submap, or back to the default ones. |
| `togglefloating` |  | Float the focused window centred at half the output, or tile it again. |
| `place` | `<left\|right\|top\|bottom\|topleft\|topright\|bottomleft\|bottomright\|fill\|center\|restore>` | Float the focused window onto that half, quarter or the whole usable area, inside the gaps and its size limits, the same rect a drag to that edge snaps to, or centre it; restore returns it to where it was, floating or tiled, before the last place or drag snap. |
| `focusfloating` | `[toggle\|on\|off]` | Move focus between the floating windows of the focused workspace (on) and its tiled ones (off); toggle unless told. |
| `fullscreen` | `[toggle\|on\|off] [fake]` | Make the focused window cover its output, above the top layer, or end that; toggle unless told. With fake the window stays where it is and is only told it is fullscreen, so a video player in a tile fills the tile. |
| `setprop` | `<id> <key> <value>` | Give the window with this IPC ID a rule key until it closes, as a rule matching only it would, after every rule: `setprop 7 border {active: red}`, `setprop 7 opacity 0.8`. The value is YAML, as in the config file. |
| `showdesktop` | `[toggle\|on\|off] [all]` | Slide the windows of the output under the pointer, or of every output with all, toward their nearest edges until only a sliver of each shows, to reach the desktop. Nothing is resized and the layout stays; keyboard focus leaves the windows. A click on a sliver, a window opening or being activated there, a workspace switch or focusing a window ends it, as does the action again. toggle unless told. |
| `swallow` | `[toggle\|on\|off]` | For a window that swallowed the terminal it was started from (see `swallow` in config.md), or that terminal: off shows the terminal again beside the window, on hides it in the window's place once more; toggle unless told. |
| `pin` | `[toggle\|on\|off]` | Keep the focused window on screen through workspace switches of its output, floating above the workspace's other floating windows; a tiled window floats first. toggle unless told; moving it to a workspace or tiling it unpins it. |
| `maximize` | `[toggle\|on\|off]` | Make the focused window fill its output's usable area inside the outer gaps, keeping bars and its border, above its workspace's other windows, or end that; toggle unless told. |
| `resizeactive` | `<dx> <dy>` | Grow or shrink the focused window by pixels; tiled windows move their right or bottom edge, else their left or top one. |
| `movewindow` |  | Mouse binds only: drag the window under the pointer; tiled windows drop into the tree where released. A window drawing its own title bar starts the same drag with xdg_toplevel.move or _NET_WM_MOVERESIZE, with the button or touch point it was pressed with. |
| `resizewindow` |  | Mouse binds only: resize the window under the pointer from its nearest corner; tiled windows change their split ratios. A window drawing its own edges starts the same drag from the edge it names with xdg_toplevel.resize or _NET_WM_MOVERESIZE. |
| `zoom` | `<in\|out\|reset\|set> [factor]` | Magnify the output under the pointer around the pointer: by factor, 1.5 unless given, from where it is going, back to 1, or to factor, up to 16. |
| `debug` | `<damage\|opaque\|planes> [toggle\|on\|off], <trace\|profile> <start <file>\|stop>, heap <file>` | Debugging a running session, bound to nothing by default. `damage` fills each frame's damage with a tint that fades over 8 frames; `opaque` tints what the culler takes as opaque; `planes` tints every composited frame, so whatever shows untinted went to direct scanout or an overlay plane. `trace` records the frame trace of `-frame-trace` to an absolute path, `profile` a Go CPU profile, and `heap` writes a heap profile once. |
| `casttarget` | `<window [id]\|output [name]\|clear>` | Point the dynamic screencast source at a window, the focused one unless given, or an output, the focused one unless given, or at nothing. A stream on that source (lys_dynamic_capture_v1) follows without stopping. When the window closes or the output goes, the target clears and the stream shows nothing until the next one. |
| `focuslast` |  | Focus the window focused before the focused one, showing its workspace; pressed again, it toggles between the two. |
| `focusurgent` |  | Focus the most recently used window that asks for attention, or the window focused before, as focuslast. |
| `cycle` | `<next\|prev> [app\|workspace\|output]` | Walk the windows in most-recently-used order, optionally only the focused app's, workspace's or output's, focusing each; the order changes only when the walk ends, at the release of the bind's modifiers or the next other input. |
| `switcher` | `<next\|prev> [app]` | Alt-Tab: select the next or previous window in most-recently-used order, optionally of the focused app; the strip of previews shows after switcher.open_delay if the bind's modifiers are still held, releasing them focuses the selection and Escape cancels. |
