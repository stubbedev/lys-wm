# Getting started

[Install](./install) lys-wm first. The built-in config binds `SUPER + Return`
to `foot`; install it or [change the bind](#configure).

## Start it

| From | Run |
| --- | --- |
| A display manager | Pick the `lys-wm` session (runs `lys-wm --systemd`) |
| A text console | `lys-wm --systemd` |
| Another Wayland session | `lys-wm` (opens a nested window) |

Nested, it prints the display it listens on:

```
lys-wm: listening on wayland-2 and X display :1 with the nested backend
```

```sh
WAYLAND_DISPLAY=wayland-2 foot
```

## Command line

| Flag | Effect |
| --- | --- |
| `--config <file>` | Config file; must exist. Default `$XDG_CONFIG_HOME/lys-wm/config.yaml`, else the built-in config |
| `--runtime <file>` | [Runtime overrides](../config#runtime-overrides) merged over the config; must exist. Default `runtime.yaml` beside the config, if present |
| `--systemd` | Export the session environment to D-Bus activation and the systemd user manager, and run `lys-wm-session.target`; see [Session](../config#session). Not for nested use |
| `--backend <name>` | `drm`, `nested` or `headless`. Default `nested` when `WAYLAND_DISPLAY` is set, else `drm` |
| `--renderer <name>` | `gles`, `vulkan` or `soft`. Default `gles`, `soft` on headless. `drm` takes only `gles` or `vulkan` |
| `--holder` | Run under a holder process so the session can be [upgraded in place](./upgrade) |
| `--check-config` | Load the config and runtime overrides as a reload would and exit; status 0 and no output when accepted |
| `--effective-config` | Print every key set, with the layer, file and line it comes from, as JSON, and exit |
| `--default-config` | Print the built-in config and exit |
| `--config-schema` | Print the config's JSON schema and exit |
| `--frame-trace <file>` | Record every frame's timing as JSON lines; see [Frame pacing](../frame-pacing) |
| `--cpu-profile <file>` | Write a Go CPU profile while running |

lys-wm logs to stderr. `LYS_WM_DEBUG=wire` adds a Wayland message trace.

## Default binds

From the built-in config (`lys-wm --default-config` prints it in full):

| Chord | Action |
| --- | --- |
| `SUPER + Return` | Run `foot` |
| `SUPER + Space` | lys-shell's launcher (global shortcut) |
| `SUPER + A` | lys-shell's dashboard (global shortcut) |
| `SUPER + Q` | Close the focused window |
| `SUPER + H J K L` or arrows | Move focus |
| `SUPER + SHIFT + H J K L` or arrows | Move the focused window |
| `SUPER + 1` … `0` | Show workspace 1–10 |
| `SUPER + SHIFT + 1` … `0` | Move the window to workspace 1–10 |
| `SUPER + D`, `S`, `T` | Wrap the window in a horizontal, vertical or tab group |
| `SUPER + F` | Fullscreen |
| `SUPER + V` | Toggle floating |
| `SUPER + Tab` | Toggle the overview |
| `ALT + Tab`, `ALT + SHIFT + Tab` | Switch windows in most-recently-used order |
| ``ALT + ` `` | Switch between the focused app's windows |
| `SUPER + R` | Resize submap: arrows resize, `Return` or `Escape` leaves |
| `SUPER + =`, `SUPER + -`, `SUPER + Backspace` | Zoom in, out, reset |
| `SUPER + CTRL` + scroll, `SUPER` + two-finger pinch | Zoom smoothly |
| `SUPER` + left / right drag | Move / resize the window under the pointer |
| `SUPER` + scroll | Step through open workspaces |
| Three-finger horizontal / vertical swipe | Switch workspaces / open the overview |
| `SUPER + SHIFT + R` | Reload the config |
| `SUPER + SHIFT + E` | Exit the session, without confirmation |

The keymap's `XF86Switch_VT_*` keys (usually `CTRL + ALT + F1`…) switch
virtual terminals.

## Configure

The config is `~/.config/lys-wm/config.yaml`. A file replaces the built-in
config entirely, binds included, so start from a copy:

```sh
mkdir -p ~/.config/lys-wm
lys-wm --default-config > ~/.config/lys-wm/config.yaml
```

Edit it, then press `SUPER + SHIFT + R` or run `lys-wmctl reload`. Nothing
reloads automatically. For example, a keyboard layout:

```yaml
input:
  keyboard:
    layout: dk
```

A file with an error is rejected whole and the running config stays; unknown
keys are skipped and the rest applies. Either way an on-screen notice names
the file, line and key until a reload fixes it. [Config](../config) lists
every key, [Actions](../actions) every action.

## Script it

```sh
lys-wmctl windows
lys-wmctl dispatch workspace 3
lys-wmctl watch
```

See [IPC](../ipc).

## A desktop around it

lys-wm draws windows only. Bars, launchers, wallpapers and portals are separate
programs, started from `autostart` or as user services wanted by
`graphical-session.target`. [lys-shell](https://stubbedev.github.io/lys-shell/)
is the lys shell and [lys-login](https://stubbedev.github.io/lys-login/) its
display manager and lock screen; any layer-shell bar and
`ext-session-lock-v1` locker also works.
