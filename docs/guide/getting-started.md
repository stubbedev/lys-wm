# Getting started

[Install](./install) lys-wm first. The built-in config binds `SUPER + Return`
to `foot`. The Debian and Fedora packages recommend it and the Nix modules enable
it; on any other install, or without recommended packages, install it too or
[change the bind](#configure) before you start a session.

## Start it

**From a display manager**, pick the `lys-wm` session, which runs
`lys-wm --systemd`. [Session](../config#session) says what the flag does.

**From a text console**, log in on a virtual terminal and run the same command:

```sh
lys-wm --systemd
```

**Inside another Wayland session**, run `lys-wm` without the flag. It opens a
window on the running compositor and prints the display it listens on:

```
lys-wm: listening on wayland-2 and X display :1 with the nested backend
```

Point clients at it:

```sh
WAYLAND_DISPLAY=wayland-2 foot
```

## Command line

| Flag | Value |
| --- | --- |
| `--config <file>` | Read this file instead of the [default path](../config); the file must exist |
| `--systemd` | Hand the environment to the user manager and run `lys-wm-session.target`; leave it off when nested |
| `--backend <name>` | `drm`, `nested` or `headless`. Default: `nested` when `WAYLAND_DISPLAY` is set, else `drm` |
| `--renderer <name>` | `gles`, `vulkan` or `soft`. Default: `gles`, and `soft` on the headless backend. `gles` and `vulkan` work on every backend, `soft` on `nested` and `headless` |

lys-wm logs to stderr. `LYS_WM_DEBUG=wire` adds a trace of the Wayland messages.

## The default binds

These come from the built-in config,
[`internal/config/default.yaml`](https://github.com/stubbedev/lys-wm/blob/main/internal/config/default.yaml),
which is the full list.

| Chord | Action |
| --- | --- |
| `SUPER + Return` | Run `foot` |
| `SUPER + Space` | Open or close lys-shell's search panel, through the global shortcut lys-shell registers |
| `SUPER + A` | Open or close lys-shell's dashboard, through the global shortcut lys-shell registers |
| `SUPER + Q` | Close the focused window |
| `SUPER + H`, `J`, `K`, `L` or the arrows | Move focus |
| `SUPER + SHIFT + H`, `J`, `K`, `L` or the arrows | Move the focused window |
| `SUPER + 1` … `0` | Show workspace 1 to 10 |
| `SUPER + SHIFT + 1` … `0` | Move the focused window to workspace 1 to 10 |
| `SUPER + D`, `S`, `T` | Wrap the focused window in a horizontal, vertical or tab group |
| `SUPER + F` | Fullscreen |
| `SUPER + V` | Float or tile the focused window |
| `SUPER + R` | Resize with the arrows; `Return` or `Escape` leaves |
| `SUPER + =`, `SUPER + -`, `SUPER + Backspace` | Zoom in, zoom out and back to normal |
| `SUPER + CTRL` and scroll, or `SUPER` and a two-finger pinch | Zoom smoothly around the pointer |
| `SUPER` + left or right drag | Move or resize the window under the pointer |
| `SUPER` + scroll | Step through the open workspaces |
| `SUPER + SHIFT + R` | Read the config file again |
| `SUPER + SHIFT + E` | Leave the session, with no confirmation |

Scripts can do the same with `lys-wmctl dispatch exit`.

The keymap's `XF86Switch_VT_*` keys, usually `CTRL + ALT + F1` and up, switch
virtual terminals.

## Configure

lys-wm has one config file, `~/.config/lys-wm/config.yaml`. A file replaces the
built-in config, binds included, so start from a copy:

```sh
mkdir -p ~/.config/lys-wm
curl -fsSL -o ~/.config/lys-wm/config.yaml \
  https://raw.githubusercontent.com/stubbedev/lys-wm/main/internal/config/default.yaml
```

Edit the copy, then press `SUPER + SHIFT + R` or run `lys-wmctl reload`. To use
another terminal, change the `SUPER + Return` bind that is already there. For a
keyboard layout, add:

```yaml
input:
  keyboard:
    layout: dk
```

A bad file is rejected whole, with the file, line and key in the error, and the
running config stays; a notice across the top of the screen says so until a
reload fixes it. A key this lys-wm does not know is left out with the same
notice, and the rest of the file applies. Nothing reloads by itself when the
file changes.
[Config](../config) has every key and [Actions](../actions) every action a bind
can run.

## Script it

`lys-wmctl` queries and controls a running lys-wm:

```sh
lys-wmctl windows
lys-wmctl dispatch workspace 3
lys-wmctl watch
```

[IPC](../ipc) has the commands, the JSON protocol and the events.

## A desktop around it

lys-wm draws windows and nothing else: bars, launchers, wallpapers and portals
are other programs, started from `autostart` in the config or as user services
that want `graphical-session.target`.
[lys-shell](https://stubbedev.github.io/lys-shell/) is the family's shell and
[lys-login](https://stubbedev.github.io/lys-login/) its display manager and lock
screen. Any layer-shell bar and `ext-session-lock-v1` locker works as well.
