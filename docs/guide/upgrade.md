# Upgrading in place

A session started with `--holder` can move to a new lys-wm binary without
logging out:

```sh
lys-wmctl upgrade "$(command -v lys-wm)"
```

## Enabling it

Add `--holder` to the command that starts the session and log in again:

```sh
lys-wm --systemd --holder
```

Without it lys-wm is one process that talks to logind itself, and
`lys-wmctl upgrade` is refused. The feature is tested on virtio-gpu in a VM;
on other GPUs, NVIDIA especially, try it when losing the session is
acceptable.

Under `--holder` each session has two processes: the holder, which keeps the
logind session and is never replaced, and the compositor. `lys-wmctl upgrade`
reports the new compositor's pid.

## What happens

Windows, workspaces, layout and clients stay. The new binary reads the config,
so this also applies config changes the old binary could not read. On
success `lys-wmctl upgrade` prints the new pid, binary, and the clients and
windows kept, and exits 0.

On any failure the session stays on the old binary, unchanged, and the command
prints why and exits 1. The new binary must use the same handover format,
accept the config, rebuild every client and present a frame before the old one
lets go.

Outputs keep their mode unless the config changes it; changes made with
`wlr-randr` since the last reload are lost, as on a reload.

## What clients see

Only things a compositor may do at any time:

- Popups and menus close.
- Clipboard and primary selection are cleared; clipboard managers set them again.
- Keyboard and pointer focus leave and return.
- Screen captures and screencasts stop; a screencast must be restarted. Gamma
  and output power controls fail, so night-light tools reapply.
- Foreign-toplevel and workspace handles close and reappear; taskbars see
  every window close and reopen in one update.
- Input methods are told they are unavailable and must bind again; client
  hotkeys are revoked and focus grabs cleared.
- Tablets and tablet tools are removed and re-added.
- A window may briefly show its last committed frame.

## What refuses an upgrade

Each reason is reported and nothing changes:

- The session is locked.
- A drag is in progress.
- The session is not on the active VT.
- Any virtual output created with `lys-wmctl output create` exists.
- A client exports or imports a window across clients (xdg-foreign, as portal
  file choosers do while open), holds a sandbox listener or runs inside a
  sandbox (security-context), or uses `wl_shell`.
- A client holds a surface whose buffer it destroyed (rare; retrying a moment
  later usually works).
- A surface commit is still pending after 5 seconds, e.g. on a GPU fence that
  never signals.
- Xwayland is running. Quit the X11 programs and pass `--drop-x11`, which
  ends Xwayland; the next X11 program starts a new one on the same `DISPLAY`.
  Xwayland keeps running once started, so any session that ran an X11 program
  needs the flag.
