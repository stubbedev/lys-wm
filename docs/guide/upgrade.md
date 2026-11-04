# Upgrading in place

After an update the running compositor is still the old binary. A session
started with `--holder` can move to the new one without logging out:

```sh
lys-wmctl upgrade "$(command -v lys-wm)"
```

## Turning it on

`--holder` is off by default: without it lys-wm runs as one process and talks to
logind itself, as it always has, and `lys-wmctl upgrade` refuses and says this.
Add the flag to the command that starts the session, then log in again:

```sh
lys-wm --systemd --holder
```

This has only been run on virtio-gpu in a VM. On other GPUs, NVIDIA's above all,
try it when losing the session would be acceptable.

## What it does

Windows, workspaces and layout stay, and clients keep running. The new binary
reads the config, so this is also how a config the old binary could not read
takes effect. `lys-wmctl upgrade` prints the new process and what it kept, and
exits 0 only when the session is on the new binary.

If anything goes wrong the session stays on the old binary, unchanged and
usable, and the command prints why and exits 1. The new binary must be a lys-wm
that hands over in the same format, must accept the config, and must rebuild
every client and show a frame before the old one lets go.

## What a client notices

Nothing that any compositor may not do at any time:

- Open popups and menus close.
- The clipboard selection is cleared, as if its owner had gone away; primary
  selection too. Clipboard managers set it again.
- Keyboard and pointer focus leave and come back.
- Screen captures and screencasts stop, and capture clients are told; a
  screencast has to be started again. Gamma and output power controls end the
  same way, so a night light tool applies itself again.
- Taskbars see every window close and reopen under the same title in one update.
- An input method daemon is told it is unavailable, and has to bind again.
- A window's content may show the frame it last committed for an instant.

Outputs keep their mode unless the config changes it; a change `wlr-randr` made
since the last reload does not survive, as with a reload.

## What refuses an upgrade

These are reported and nothing changes:

- The session is locked. Unlock it first.
- A client exports a window to another client (xdg-foreign, as portals' file
  choosers do while open), or holds a sandbox listener (Flatpak, through
  security-context).
- A client uses color management, when `color_management` is on.
- A client holds a surface whose buffer it has destroyed. This is rare, and
  repeating the command a moment later usually succeeds.
- A surface's commit is still waiting after five seconds, for instance on a GPU
  fence that does not signal.
- Xwayland is running, and every X11 client would be lost. Quit them and pass
  `--drop-x11`, which ends Xwayland; the next X11 program starts a new one on
  the same `DISPLAY`. Xwayland stays running once started, so a session that ever
  ran an X11 program needs the flag.

## Process model

Under `--holder`, `pgrep lys-wm` shows two processes per session: the holder the display manager
started, and the compositor. After an upgrade the holder is still the old binary,
as it holds the logind session and has nothing to update; the compositor is the
new one. `lys-wmctl upgrade` reports its pid.
