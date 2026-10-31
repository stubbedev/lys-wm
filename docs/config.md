# Config

lys-wm reads one YAML file: `$XDG_CONFIG_HOME/lys-wm/config.yaml`, or
`~/.config/lys-wm/config.yaml` when `XDG_CONFIG_HOME` is unset. `lys-wm --config
<file>` reads another file instead, and that file must exist. With no file at
the default path, lys-wm runs the built-in config,
[`internal/config/default.yaml`](../internal/config/default.yaml), which is a
good starting point to copy.

Unknown keys are errors. Every error gives the file, the line and the key:

```
config.yaml:12: binds[3].action: bad action: unknown action "teleport"
```

`SIGHUP` and the `reload` action read the file again. A file that does not
parse, or whose chords, layout or font do not load, is rejected whole and the
running config stays. Nothing reloads when the file changes.

Every section is optional. An empty file gives the defaults.

## outputs

A list of rules. Each output takes the first rule that matches it, or the
rule with `match: "*"` when none does; without one, it runs at its preferred
mode and scale 1. Only one rule can use `"*"`.

| Key | Value |
| --- | --- |
| `match` | A connector name (`DP-1`), `desc:` and the start of the output's description: its EDID make, model and serial, as in `desc:Samsung Electric Company LC32G5xT`, or `"*"` for every output no other rule names |
| `mode` | `preferred` (the default), `highres` (the largest mode, then the fastest), `<width>x<height>`, or `<width>x<height>@<hz>`. Without a rate, the fastest mode of that size; with one, the closest within 1 Hz |
| `position` | `<x>x<y>` in logical pixels, or `auto` (the default): outputs without a position line up to the right of all the others |
| `scale` | 0.25 to 10, in steps of 1/120. Default 1 |
| `bitdepth` | `8` (the default) or `10`: draw frames in 10 bits per channel, for smoother gradients under a colour transform such as night light. Some drivers break screen sharing or fullscreen games with it; an output that cannot show 10-bit frames stays at 8 and says so in the log |
| `disabled` | `true` turns the output off |

A mode the output does not have is reported, and the output keeps its current
mode.

Display tools such as wlr-randr, kanshi, wdisplays and nwg-displays change
outputs while lys-wm runs, through wlr-output-management. Their changes take the
place of these rules for the outputs they name until the config is reloaded,
which applies the rules again. lys-wm refuses a configuration that rotates an
output, turns on adaptive sync, uses a mode the output does not list, or turns
every output off.

While the laptop lid is closed, internal panels (`eDP-*`) are off whatever the
rules say, and their workspaces move to the other outputs; opening the lid
brings the panel back under its rule. libinput turns off the internal keyboard
and touchpad while the lid is closed.

## input.keyboard

| Key | Value |
| --- | --- |
| `rules`, `model`, `layout`, `variant`, `options` | The XKB names, as in `setxkbmap`. With none set, the `XKB_DEFAULT_*` variables apply |
| `repeat_rate` | Repeats per second; 0 turns repeat off. Default 50 |
| `repeat_delay` | Milliseconds before repeat starts. Default 300 |

## input.pointer, input.touchpad and input.devices

`input.touchpad` applies to touchpads (devices that can tap), `input.pointer` to
every other pointing device. `input.devices` maps a libinput device name, as
`libinput list-devices` prints it, to settings that override both for that
device.

```yaml
input:
  pointer:
    accel_profile: flat
  touchpad:
    tap: true
    natural_scroll: true
  devices:
    "Logitech MX Master 3":
      scroll_factor: 0.5
```

| Key | Value |
| --- | --- |
| `tap`, `drag_lock` | Tap to click, and keep a tap-and-drag going after the finger lifts |
| `natural_scroll` | Content follows the fingers |
| `disable_while_typing` | Ignore the touchpad while the keyboard is in use |
| `middle_emulation` | Pressing left and right together is a middle click |
| `click_method` | `button_areas`, `clickfinger` or `none` |
| `scroll_method` | `two_finger`, `edge`, `on_button_down` or `none` |
| `accel_profile` | `adaptive` or `flat` |
| `accel_speed` | -1 to 1 |
| `scroll_factor` | Multiplies scroll distances; above 0, up to 100. Default 1 |

An unset key keeps libinput's default for the device, and a reload that drops a
key restores that default. A device that lacks a setting from `input.pointer`
or `input.touchpad` skips it; one that lacks a setting named for it under
`input.devices` is reported. These settings apply with the drm backend; nested,
the host compositor configures the devices.

## Colours

A colour is a palette name or `#rrggbb[aa]`. The palette starts as
[Catppuccin Mocha](https://catppuccin.com/palette) (`mauve`, `crust`,
`surface0`, …); `palette` adds names or overrides them:

```yaml
palette:
  accent: "#ff8800"
```

## borders

| Key | Default |
| --- | --- |
| `width` | 1 |
| `active` | `mauve` |
| `inactive` | `crust` |

`active` and `inactive` take a colour, or a gradient of 2 to 10 colours spread
evenly across the border along `angle` degrees (0, the default, runs left to
right; angles turn clockwise):

```yaml
borders:
  active: {colors: [mauve, blue], angle: 45}
```

## decoration

```yaml
decoration:
  rounding: 8
```

| Key | Default |
| --- | --- |
| `rounding` | 0: the radius of window corners in logical pixels, scaled with the output. Surfaces are clipped to it, antialiased, and the border follows it with an outer radius of `rounding` plus the border width. Fullscreen windows stay square |
| `active_opacity` | 1: the focused window's opacity, 0 to 1 |
| `inactive_opacity` | 1: every other window's opacity |
| `fullscreen_opacity` | 1: a fullscreen window's opacity, whether focused or not |
| `dim_inactive` | `false`: darken every window but the focused one |
| `dim_strength` | 0.5: how dark, 0 to 1, for `dim_inactive` and `dim_around` |

A rounded window shows what is behind its corners, so it is always
composited, never scanned out directly, unless it is fullscreen.

`decoration.shadow` draws a soft shadow under every window but fullscreen
ones, following its rounded corners:

```yaml
decoration:
  shadow:
    enabled: true
    range: 12
    render_power: 3
    offset: [2, 4]
    color: "#1a1a1aee"
```

| Key | Default |
| --- | --- |
| `enabled` | `false` |
| `range` | 4: how far the shadow fades out beyond its edge, in logical pixels |
| `render_power` | 3: how fast it fades, 1 (linearly) to 4 (quickly, with a darker core) |
| `sharp` | `false`: `true` gives a hard-edged shadow with no fade |
| `offset` | `[0, 0]`: where the shadow sits relative to the window, in logical pixels |
| `scale` | 1: the shadow's size relative to the window, above 0 to 1 |
| `color` | `#1a1a1aee` |
| `color_inactive` | `color`: the shadow of windows that are not focused |

The shadow is cut out under the window, so a translucent window does not show
its own shadow through itself.

`decoration.blur` blurs what lies behind translucent windows, and behind the
layer surfaces whose namespace it lists:

```yaml
decoration:
  blur:
    enabled: true
    size: 8
    passes: 2
    layers: [waybar, rofi]
```

| Key | Default |
| --- | --- |
| `enabled` | `false`: blur behind every window that is translucent, through its opacity or its own alpha |
| `size` | 8: the distance between the blur's taps, in logical pixels |
| `passes` | 1: how many times it halves and doubles, 1 to 4; each pass doubles the reach |
| `layers` | none: layer-shell namespaces to blur behind, as `waybar` or `rofi` name themselves |
| `xray` | `false`: blur only the background and bottom layers (the wallpaper) behind windows and top layer surfaces, never the windows below them; every window then shares one blur per output, which windows moving underneath never redraw |

A window rule's `blur: true` or `blur: false` overrides `enabled` for the
windows it matches. The blur follows the window's rounded corners. It is
redrawn only when what lies behind the window, within the blur's reach,
changes, and then the whole window is repainted.

Opacity multiplies what the client draws, including the alpha it asks for
through alpha-modifier; borders stay opaque. A window rule's `opacity` takes
the place of these for the windows it matches. A translucent window shows
what is below it, so it is never scanned out directly.

## gaps

```yaml
gaps:
  inner: 6
  outer: [12, 8, 12, 8]
```

| Key | Default |
| --- | --- |
| `inner` | 0: logical pixels between neighbouring tiled windows, borders included |
| `outer` | 0: logical pixels between the tiled windows and the edges of the output's usable area; one number for every side, or `[top, right, bottom, left]` |

Floating and fullscreen windows ignore gaps; a tab group's strip and its windows
sit inside the group's tile.

## animations

```yaml
animations:
  enabled: true
  curves:
    snappy: {bezier: [0.05, 0.9, 0.1, 1.05]}
    wobbly: {spring: {stiffness: 200, damping: 12, mass: 1}}
  events:
    global: {curve: snappy, duration: 300}
    windows_move: {curve: wobbly}
```

Off unless `enabled: true`.

`curves` names curves for the events to use. A curve is either a cubic
`bezier: [x1, y1, x2, y2]`, as in CSS `cubic-bezier()`, whose x coordinates lie
in 0 to 1 and whose y coordinates may leave 0 to 1 to overshoot, or a damped
`spring: {stiffness, damping, mass}` (all above 0; `mass` defaults to 1), which
settles in its own time: lower damping bounces, `damping` near
`2 * sqrt(stiffness * mass)` settles fastest without overshooting. Built in, and
not redefinable: `linear`, `ease`, `ease_in`, `ease_out`, `ease_in_out` (the CSS
curves) and `spring` (stiffness 170, damping 26).

`events` configures each event with `enabled`, `curve`, `duration`
(milliseconds, for bézier curves; a spring takes none) and, where the table
lists styles, `style`. Each key an event leaves out comes from its parent;
`style` comes from the event's own default:

| Event | Parent | Animates | Styles |
| --- | --- | --- | --- |
| `global` | | Defaults: `ease_out` over 250 ms | |
| `windows` | `global` | Every window animation | |
| `windows_move` | `windows` | A window's tile moving or resizing: its surfaces, popups and border are drawn at the tile in between, while the client draws at the new size at once | `morph`: a blob in the window's border colour stretches ahead of it toward where it is going, melting into the window like a drop of liquid; `elastic`: a window dragged with the mouse bends like jelly, the part under the pointer following it and the rest catching up on a spring (`windows_move`'s, when its curve is a spring) until it settles flat |
| `windows_in` | `windows` | A window appearing | `popin <percent>` (the default, `popin 80%`): it grows from that share of its size at its centre; `slide`: it slides in from the nearest edge of its output |
| `windows_out` | `windows` | A window closing: its last frame plays the same styles in reverse, taking no input | `popin <percent>`, `slide` |
| `fade` | `global` | Every fade | |
| `fade_in` | `fade` | A window's opacity, border and shadow fading in as it appears | |
| `fade_out` | `fade` | A closing window fading out | |
| `fade_switch` | `fade` | A window's opacity changing between `active_opacity` and `inactive_opacity` as focus moves | |
| `fade_dim` | `fade` | `dim_inactive`'s dim coming and going as focus moves | |
| `fade_shadow` | `fade` | A shadow's colour changing between `color` and `color_inactive` | |
| `border` | `global` | A border changing between `borders.active` and `borders.inactive` as focus moves: the new border fades in over the old one | |
| `borderangle` | `global` | A gradient border's angle turning | `once` (the default): one full turn whenever the window gains or loses focus; `loop`: a full turn per `duration` for as long as the window is shown, which keeps its output drawing, but only the border |
| `workspaces` | `global` | Switching an output to another workspace: the workspace it leaves stays drawn, taking no input, until the switch ends; layer surfaces and the cursor stay put | `slide` (the default), `slidevert`: both workspaces move along the workspace order, horizontally or vertically; `fade`: they cross-fade; `popin <percent>`: the new one grows from that share of the output and fades in while the old one fades out; `goo`: they cross-fade while each window of the old workspace flows into the nearest window of the new one as a liquid blob in its border colour |
| `layers` | `global` | Every layer-shell surface animation (bars, launchers, notifications) | |
| `layers_in` | `layers` | A layer surface mapping; its exclusive zone applies at once, so windows do not wait for it | `slide` (the default): in from the edge it is anchored to, or the nearest edge; `popin <percent>`; `fade` |
| `layers_out` | `layers` | A layer surface unmapping: its last frame plays out, taking no input | `slide`, `popin <percent>`, `fade` |

Animations run on each output's frame clock: every frame shows the animation
at the time it starts drawing, and an output stops drawing once its
animations end. A spring keeps its velocity when its target changes mid-flight, so a window
retargeted while it moves carries on smoothly. A window being dragged or resized
with the mouse follows the pointer without animating, unless `windows_move` uses
a spring: then the window trails the pointer on that spring and settles where
it is dropped, and a window that comes back with its workspace
is drawn where it belongs at once.

## tabs

| Key | Default |
| --- | --- |
| `height` | 15 |
| `padding` | 5, between the tab strip and the windows |
| `text_height` | 8 |
| `text_padding` | 3 |
| `font` | A TrueType file. Default: `LYS_WM_FONT`, then the DejaVu Sans the package was built with |
| `icon_theme` | The freedesktop icon theme tab icons named by windows resolve in, searched in the XDG data directories. Default: `Adwaita`, which falls back to `hicolor` |
| `active`, `inactive`, `urgent` | `background` and `text` colours, both required. Defaults `mauve`/`crust`, `surface0`/`text` and `red`/`crust` |

A tab shows the icon its window set through `xdg_toplevel_icon_v1` before
the title, as a square as tall as the tab less `text_padding` on each side,
when the tab is at least four icons wide. An icon name that resolves in
`icon_theme` wins; otherwise the window's own pixel buffers are drawn.

## autotile

| Key | Default |
| --- | --- |
| `enabled` | `true` |
| `ephemeral` | `true`: groups autotile creates dissolve when one window is left |
| `min_trigger` | 400 |
| `divisor` | 3 |

When one more column would make the columns narrower than `min_trigger` or
the output width divided by `divisor`, whichever is larger, the new window
stacks under the focused one instead.

## binds and submaps

```yaml
binds:
  - chord: SUPER + SHIFT + Q
    action: kill
  - {chord: SUPER + R, action: submap resize}
submaps:
  resize:
    - {chord: Right, action: resizeactive 20 0, flags: [repeating]}
    - {chord: Escape, action: submap reset}
```

A chord is modifiers and a trigger joined by `+`. Modifiers are `SUPER`
(`MOD4`, `WIN`, `LOGO`), `SHIFT`, `CTRL` (`CONTROL`) and `ALT` (`MOD1`). The
trigger is a keysym from the first layout (`Return`, `Q`, `bracketleft`,
`XF86AudioRaiseVolume`), `mouse:<button code>` (`mouse:272` is the left
button) or `mouse_up`/`mouse_down` for the scroll wheel.

`flags` takes `locked` (the bind works while the session is locked) and
`repeating` (the action repeats while the key is held, at the keyboard
repeat rate).

A focused window that inhibits shortcuts (virtual machines, remote desktop
clients) receives every key, button and scroll itself; only `locked` binds
still run, so give the bind that takes focus back from such a window the
`locked` flag.

Actions are listed in [actions.md](actions.md).

## rules

```yaml
rules:
  - {app_id: ^steam$, float: true}
  - {app_id: ^steam$, title: ^Friends List$, size: 400x900}
```

`app_id` and `title` are regular expressions ([RE2](https://github.com/google/re2/wiki/Syntax));
a rule needs at least one. `float: true` or `tile: true` decides where a new
window goes; `size: <w>x<h>` sizes it when it floats. `dim_around: true` darkens everything below the window by
`decoration.dim_strength`, as for a dialog that should hold the attention.
`blur: true` or `blur: false` turns `decoration.blur` on or off for the window.
`opacity: <0 to 1>` sets the window's opacity, focused or not, in place of
`decoration`'s. `tearing: true` or
`tearing: false` overrides what the window asks for through
`wp_tearing_control_v1` (see [tearing](#tearing)). Later rules override
earlier ones.

X11 windows match `app_id` against the class half of `WM_CLASS` (`Steam`,
`jetbrains-idea`). Without a rule, a window floats when it has a parent
(`xdg_toplevel.set_parent` or xdg-foreign; `WM_TRANSIENT_FOR` for X11),
has a fixed size (its minimum size equals its maximum; `WM_NORMAL_HINTS`
for X11), is an `xdg_dialog_v1`, or, for X11, is modal or a dialog,
utility, splash, toolbar, menu, combo, tooltip, notification or
drag-and-drop window; `tile: true` overrides that. The
choice is made again whenever the window's title, app_id, parent or size
limits change, until you float or tile the window yourself.

## env and autostart

```yaml
env:
  QT_QPA_PLATFORM: wayland;xcb
autostart:
  - waybar
```

lys-wm sets `env` in its own environment at startup, before it starts
anything, so every program it runs inherits it. `XDG_CURRENT_DESKTOP=lys-wm`
and `XDG_SESSION_TYPE=wayland` are set unless `env` overrides them.
`WAYLAND_DISPLAY`, `DISPLAY` and `LYS_WM_SOCKET` belong to lys-wm and cannot be
set here. `XCURSOR_THEME`, `XCURSOR_SIZE` and `XCURSOR_PATH` set here choose the
pointer for lys-wm as well as for its clients. Each `autostart` command runs once with `sh -c`, after the display
and the IPC socket are up. A reload changes neither. Autostart commands and
`exec` binds write to lys-wm's own stdout and stderr, and lys-wm logs any that
exits with a non-zero status or dies from a signal.

## xwayland

```yaml
xwayland:
  enabled: true
  zero_scaling: true
```

X11 programs run through Xwayland. lys-wm takes a free X display (`:0` and
up, skipping displays whose lock in `/tmp` belongs to a live process),
listens on its sockets, and exports `DISPLAY`; Xwayland itself starts only
when the first X client connects, so a session without X programs never
runs it. If Xwayland exits, the next X client starts it again. `enabled:
false` leaves `DISPLAY` unset and binds no X sockets.

With `zero_scaling: true` (the default), X programs see every output at its
physical resolution with scale 1 and draw at full sharpness; on an output
scaled to 1.5 they look smaller unless they scale themselves (`GDK_SCALE`,
`QT_SCALE_FACTOR`, Steam's and JetBrains' own settings). With `false`,
Xwayland sees the logical layout and lys-wm stretches X windows, which keeps
their size but blurs them on scaled outputs. Both settings are read at
startup; a reload does not change them.

## tearing

```yaml
tearing: true
rules:
  - {app_id: ^cs2$, tearing: true}
```

Off by default. With `tearing: true`, a window that fills an output and
scans out directly is presented with an async page flip, so frames show the
moment they are ready and may tear, when a rule says `tearing: true`, when it asks
for that through `wp_tearing_control_v1` (Mesa's Vulkan `IMMEDIATE` present
mode does), or when it says nothing there and tags its surface as a game
through `wp_content_type_v1`. A rule with `tearing: false` keeps a window on
vblank whatever it asks, and so does a vsync hint without a rule. Drivers without atomic async flips (Linux before
6.8), and frames that change more than the buffer, flip at vblank as
usual.

## lid_suspend

```yaml
lid_suspend: false
```

On by default with the drm backend: lys-wm takes logind's
`handle-lid-switch` inhibitor and decides itself what the lid does, so
logind's `HandleLidSwitch*` settings do not apply while lys-wm runs. It
suspends through logind when the lid is closed, no output other than an
internal panel (`eDP-*`) is connected, and the machine runs on battery: a
`Mains` or `USB` power supply exists and none is online. It checks when the
lid closes, when an output is unplugged and when a power supply changes, so
undocking or unplugging the charger with the lid already closed suspends too,
which logind alone never does. A machine without such a power supply never
counts as on battery. `false` leaves the lid to logind. Read at startup only.

## gpu

```yaml
gpu: card1
```

The DRM card that renders, as `card1` or `/dev/dri/card1`. Without it lys-wm
picks the boot VGA device if it has a render node, else the first card with
a render node. Every other card shows its outputs through a copy from this
one. Read at startup only.

## Locking

lys-wm speaks `ext-session-lock-v1`, so lockers such as swaylock, hyprlock or
lys-shell's lock screen work. While the session is locked, nothing but the lock
surfaces renders or receives input, and only binds flagged `locked` run.
lys-wm tells the locker it is locked only after every output has drawn a
blank frame. If the locker dies, the session stays locked on a dark red
screen until a new locker takes over; outputs added while locked show the
lock colour until the locker covers them. lys-wm does not set logind's
`LockedHint`: the locker does, as lys-shell does today.

## Session

`lys-wm --systemd` hands `env`, `PATH`, `WAYLAND_DISPLAY`, `DISPLAY` and
`LYS_WM_SOCKET` to D-Bus activation and the systemd user manager, so user
services find the same commands the session does; a variable `env` sets wins
over lys-wm's own. It then starts `lys-wm-session.target`, which binds
`graphical-session.target`, so user services that want a graphical session
start with lys-wm. On exit lys-wm stops the target and puts the user manager's
environment back as it found it: a variable it had before gets its old value,
one it did not have is removed. D-Bus cannot unset a variable, so the
activation environment keeps them.

A display manager session should run `lys-wm --systemd`. Leave the flag off
when running lys-wm nested in another session, which owns
`graphical-session.target`.

Portals follow `XDG_CURRENT_DESKTOP=lys-wm`: the package installs
`lys-wm-portals.conf`, which routes every portal to lys-shell's backend and falls
back to xdg-desktop-portal-wlr for screen sharing and screenshots and to
xdg-desktop-portal-gtk for the rest. Portal backends are started by D-Bus
activation, so they reach the session only when lys-wm runs with `--systemd`.
