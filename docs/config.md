# Config

lys-wm reads one YAML file: `$XDG_CONFIG_HOME/lys-wm/config.yaml`, or
`~/.config/lys-wm/config.yaml` when `XDG_CONFIG_HOME` is unset.
`lys-wm --config <file>` reads another file instead, and that file must exist.
With no file at the default path, lys-wm runs the built-in config,
[`internal/config/default.yaml`](https://github.com/stubbedev/lys-wm/blob/main/internal/config/default.yaml),
which is a good starting point to copy.

Unknown keys are errors. Every error gives the file, the line and the key:

```
config.yaml:12: binds[3].action: bad action: unknown action "teleport"
```

`SIGHUP` and the `reload` action read the file again. A file that does not
parse, or whose chords, layout or font do not load, is rejected whole and the
running config stays. Nothing reloads when the file changes.

Every section is optional, and a file replaces the built-in config rather than
adding to it: each key the file leaves out takes the default this page gives,
and a file without `binds` has no binds and without `env` none of the built-in
config's variables.

## The default look

An empty file, or none, looks finished: rounded corners, gaps, a soft shadow, a
hairline border with the focused window's in the accent colour, and spring
animations tuned per event. Every default is on this page, and any key you set
wins over it, on every renderer.

**A config you already have changes.** Rounding, gaps, shadows, borders,
tab strips, animations and the look of blur used to be off, zero or a Catppuccin
colour unless set. A file that sets only some of them now gets these defaults for
the rest. To have the old look back:

```yaml
decoration:
  rounding: 0
  shadow: {enabled: false}
  blur: {size: 8, passes: 1, saturation: 1}
gaps: {inner: 0, outer: 0}
borders: {active: "#cba6f7", inactive: "#11111b"}
tabs:
  height: 15
  padding: 5
  text_height: 8
  text_padding: 3
  active: {background: "#cba6f7", text: "#11111b"}
animations: {enabled: false}
```

`animations: {enabled: true}` no longer means bezier curves: an event you do not
configure gets its spring, unless a parent event in your file names a curve or a
duration. See [animations](#animations).

What a key left out defaults to depends on what draws the frames, decided when
lys-wm starts, since an effect that is cheap on a GPU can eat a CPU:

| Renderer | Default look |
| --- | --- |
| GLES or Vulkan on a GPU | Everything, and blur for clients that ask for it |
| GLES on llvmpipe or softpipe, Vulkan on lavapipe or SwiftShader | Everything but blur, the overview's included. Blur costs these 10 to 40 ms per redraw. `ext_background_effect_v1` is not offered as able to blur, so clients such as lys-shell show their opaque style |
| lys-wm's software renderer (`-renderer soft`) | Also no shadow and no animated workspace switch: a 1080p frame takes 68 ms with shadows and 14 ms without, and a slide draws two workspaces. Windows and layer surfaces still animate |

Setting `decoration.blur.enabled` or `decoration.blur.layers`, either way, puts
blur back in your hands on any renderer, `overview.blur` the overview's, as does `decoration.shadow.enabled` for
shadows. `animations.events.workspaces` does the same for a workspace switch.

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
| `hdr` | `true` drives the output with HDR, as [HDR](#hdr) describes. Default `false` |
| `sdr_brightness` | With `hdr`: the cd/m² SDR white shows at, 1 to 10000. Default `203` |
| `hdr_peak` | With `hdr`: the display's peak in cd/m², 1 to 10000, for one whose EDID has it wrong. Default what the EDID says, else 400 |
| `disabled` | `true` turns the output off |
| `shader` | A fragment shader for the output's final image: see [Screen shaders](./shaders). A shader that does not compile leaves the output unshaded and says why. Reloading the config reads the file again |

A mode the output does not have is reported, and the output keeps its current
mode.

### HDR

```yaml
outputs:
  - match: DP-1
    hdr: true
    sdr_brightness: 250
```

An output with `hdr: true` carries PQ and BT.2020 at 10 bits, with the panel's
primaries and luminance from its EDID as static metadata, and overrides
`bitdepth`. Everything SDR shows at `sdr_brightness`, HDR clients at their own
levels anchored to it, and light above the panel's peak compresses into it.
An output stays SDR, and the log says why, when the backend is not DRM or
headless, the connector lacks `HDR_OUTPUT_METADATA` or `Colorspace`, the EDID lists
no ST 2084 or no BT.2020 RGB, the renderer has no float targets, or the kernel
refuses the modeset.

On an HDR output the cursor draws in the frame, so moving it repaints; a screen
shader stays off, reported in the log, as shaders take SDR code values; only a
fullscreen buffer tagged PQ and BT.2020 that fits the panel and shares the
reference white scans out directly; `zwlr_gamma_control_manager_v1` refuses it,
as gamma ramps act on code values; and a colour matrix applies in linear light.
Screenshots and screen sharing get SDR on every output: HDR content tone-mapped,
and SDR content with its own pixels, except what blurs over HDR light.

Clients see `wp_color_manager_v1` when any output has `hdr: true`, or with
`color_management: true` at the top of the file, which also gives SDR outputs
tone mapping for HDR clients. Without either nothing is offered and nothing is
converted.

Translucency, shadows and blur composite in linear light on an HDR output, which
is physically right but looks lighter than the same config on SDR.

The software renderer keeps 8 bits a channel of a client's 10-bit buffers and
evaluates transforms per pixel in Go: it is for tests. ICC profiles work when
they are matrix-shaper with one tone curve that is parametric or fits a gamma or
sRGB within 1/255; others fail with `unsupported`, as the protocol has it.

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
| `natural_scroll` | Content follows the fingers; also sets which way [gestures](#gestures) go |
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
| `active` | `#e0947a`, lys-shell's accent |
| `inactive` | `#ffffff14`, a white hairline |

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
| `rounding` | 8: the radius of window corners in logical pixels, scaled with the output. Surfaces are clipped to it, antialiased, and the border follows it with an outer radius of `rounding` plus the border width. Fullscreen windows stay square |
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
| `enabled` | `true`, but see [the default look](#the-default-look) |
| `range` | 24: how far the shadow fades out beyond its edge, in logical pixels |
| `render_power` | 2: how fast it fades, 1 (linearly) to 4 (quickly, with a darker core) |
| `sharp` | `false`: `true` gives a hard-edged shadow with no fade |
| `offset` | `[0, 8]`: where the shadow sits relative to the window, in logical pixels |
| `scale` | 1: the shadow's size relative to the window, above 0 to 1 |
| `color` | `#00000060` |
| `color_inactive` | `#00000038`, or `color` when the file sets that: the shadow of windows that are not focused |

The shadow is cut out under the window, so a translucent window does not show
its own shadow through itself.

`decoration.blur` blurs what lies behind translucent windows, the
translucent layer surfaces whose namespace it lists, and the translucent
popups of both:

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
| `enabled` | `false`: blur behind every window that is translucent, through its opacity or its own alpha, and its popups |
| `size` | 6: the distance between the blur's taps, in logical pixels |
| `passes` | 2: how many times it halves and doubles, 1 to 4; each pass doubles the reach |
| `layers` | none: layer-shell namespaces to blur behind, as `waybar` or `rofi` name themselves, and their popups | fade |
| `xray` | `false`: blur only the background and bottom layers (the wallpaper) behind windows, popups, top layer surfaces and client blur regions, never the windows below them; they then share one blur per output, which windows moving underneath never redraw |

A window rule's `blur: true` or `blur: false` overrides `enabled` for the
windows it matches and their popups. The blur follows the window's rounded
corners. A popup is blurred across its window geometry, so not behind the
shadow margin its client draws, rounded by `decoration.rounding`. A blur is
redrawn only when what lies behind it, within the blur's reach, changes, and
then the blurred area is repainted.

Clients ask for blur themselves through `ext_background_effect_v1`, which
name the exact region of a surface to blur: a toplevel, a popup, a layer
surface, a subsurface or an input method's popup. The region is honoured
whether or not `enabled` is set, with this section's `size`, `passes` and
`xray`, and it follows the window's rounded corners. Only a renderer that rasterizes on the CPU
with neither `enabled` nor `layers` in the config withholds it: the global then
offers no blur, and a region draws nothing, until a reload sets one. Where a surface has one,
it replaces the whole-surface blur `enabled` or `layers` would draw. A window
rule's `blur: false` turns regions off for that window and its popups, and
nothing else does.
Cursors, drag icons and session-lock surfaces ignore regions. The blur fades
with a surface's alpha-modifier, not with a window's `opacity`, which leaves the
blur behind a translucent window at full strength.

The same section gives glass its look, for windows, listed layers, popups and
client regions alike. Only saturation is on by default, and an effect that is off
adds nothing to a frame: with `saturation: 1` the pixels are those of a plain blur.

```yaml
decoration:
  blur:
    enabled: true
    tint: "#1e1e2e40"
    saturation: 1.4
    brightness: 1.05
    contrast: 1.1
    grain: 0.04
    refraction: {bezel: 16, amount: 8}
    specular: {strength: 0.6, width: 1}
    mica: {enabled: false, size: 16, passes: 4, tint: "#1e1e2ec0"}
```

| Key | Default |
| --- | --- |
| `tint` | none: a colour laid over the blurred backdrop, under the client |
| `saturation` | 1.3: `saturate()`, 0 to 4, as CSS filters define it |
| `brightness` | 1: `brightness()`, 0 to 4 |
| `contrast` | 1: `contrast()`, 0 to 4 |
| `grain` | 0: luminance noise over the backdrop, 0 to 1, the same pixels as lys-ui's `-lys-grain` at that opacity with the surface's corner as origin |
| `refraction.bezel` | 16 when `refraction` is set: how far in from the edge the backdrop bends, in logical pixels, as lys-ui's `-lys-refract()` does; 0 turns it off |
| `refraction.amount` | 8 when `refraction` is set: how far it looks past the edge at the rim, in logical pixels; 0 turns it off |
| `specular.strength` | 0.6 when `specular` is set: the brightness of a sheen on the rim, 0 to 1, drawn over the client |
| `specular.width` | 1 when `specular` is set: the rim's width in logical pixels |

Saturation, brightness and contrast apply in that order to what is already
blurred, then the tint, then the grain, so changing them costs no blur. The bezel runs along the window's
rounded corners, or the bounding box of a client's region on a surface without
them. The sheen is brightest on the side of the surface that faces the pointer,
with a fainter echo opposite, and turns in steps of 5.6 degrees: a pointer move
redraws only the rims it turned, and a move that turns none draws nothing. A
moving window turns its sheen as well. With `animations.reduced_motion` or
without `animations.enabled`, the light rests at the top left and never moves.

`mica` draws a window's translucent parts over a heavily blurred sample of the
background and bottom layers, as `xray` does, instead of over what lies behind
the window. `tint`, when set, replaces `decoration.blur.tint` for those windows,
and the rest of the look applies. All mica windows on an output share one blur,
which windows moving underneath never redraw; it is `xray`'s chain when `size`
and `passes` match `decoration.blur`'s. `size` is 16 and `passes` 4 by default.
Mica is for a window whose client leaves its background translucent; an opaque
client hides it, as it hides any blur. A window's popups blur as usual, since
they float over the window itself.

A window that closes keeps its frost during its exit animation, with the blur
and look it had when it closed, scaled and faded with the snapshot. A surface
hidden behind an opaque window costs nothing: its blur is not redrawn, and frees
its memory until it shows again.

Opacity multiplies what the client draws, including the alpha it asks for
through alpha-modifier; borders stay opaque. A window rule's `opacity` takes
the place of these for the windows it matches. A translucent window shows
what is below it, so it is never scanned out directly.

## drag_preview

```yaml
drag_preview:
  enabled: true
  opacity: 0.25
  blur: false
  snap_distance: 16
```

While a window is dragged with `movewindow`, a translucent rounded rectangle with a
border in `borders.active` shows where it will land if dropped now, above the tiled
windows and below the floating ones. For a tiled window that is its place in the
tree, what `movewindow` does on release, and nothing while that is where it already
is. A floating window lands under the pointer unless the pointer is near an edge of
the usable area, the output less its bars: then it snaps, and the preview shows where.
An edge shared with another output does not snap, so a window can cross it.

| Pointer within `snap_distance` of | Window lands on |
| --- | --- |
| The left or right edge | That half |
| The bottom edge | The bottom half |
| The top edge | The whole usable area |
| A corner | That quarter |

A window with size limits keeps them, and gaps apply as they do to tiled windows.
The rectangle moves on the [`snap` event](#animations) and fades on `fade_snap`.

| Key | Default |
| --- | --- |
| `enabled` | `true`. Off, a dragged window neither shows a preview nor snaps |
| `opacity` | 0.25: of the fill; the border is drawn at full strength |
| `blur` | `false`: blur what is behind the rectangle with `decoration.blur`'s `size`, `passes` and look |
| `snap_distance` | 16: logical pixels |

It costs nothing outside a drag.

## gaps

```yaml
gaps:
  inner: 6
  outer: [12, 8, 12, 8]
```

| Key | Default |
| --- | --- |
| `inner` | 8: logical pixels between neighbouring tiled windows, borders included |
| `outer` | 8: logical pixels between the tiled windows and the edges of the output's usable area; one number for every side, or `[top, right, bottom, left]` |

Floating and fullscreen windows ignore gaps; a tab group's strip and its windows
sit inside the group's tile.

## animations

```yaml
animations:
  enabled: true
  reduced_motion: false
  curves:
    snappy: {bezier: [0.05, 0.9, 0.1, 1.05]}
    wobbly: {spring: {stiffness: 200, damping: 12, mass: 1}}
  events:
    global: {curve: snappy, duration: 300}
    windows_move: {curve: wobbly}
```

On unless `enabled: false`.

`reduced_motion: true` is for people whom motion on screen bothers. Things no
longer travel, stretch, bounce or follow the pointer: what the table below marks
**still** happens at once, and what it marks **fade** or **cross-fade** keeps its
`enabled` and `duration` as a plain `ease_out` fade without overshoot, whichever
`curve` and `style` it was given. A window still fades in and out, so state changes
stay readable; specular light rests at the top left. It applies to every event,
including any added later, and a reload applies it. `lys-wmctl reducedmotion` and
the `reducedmotion` IPC event report it; lys-shell follows that and publishes it to
apps as `reduced-motion` in the portal's `org.freedesktop.appearance` namespace, so
in a lys-wm session this one key sets it for the desktop.

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
lists styles, `style`. Each key an event leaves out comes from its parent, or
from the event's own default motion below if neither it nor a parent's key sets
that; a `duration` with no curve in the file runs `ease_out`, since a spring takes
none. `style` comes from the event's own default.

With no config each event has a spring of its own, tuned to how far things travel
and how much attention they should draw:

| Event | Spring (stiffness, damping) | Overshoot | 95% there at | Style |
| --- | --- | --- | --- | --- |
| `windows_move` | 380, 34 | 0.4% | 197 ms | |
| `windows_in` | 420, 30 | 3.4% | 149 ms | `popin 92%` |
| `windows_out` | 700, 48 | 0.1% | 154 ms | `popin 94%` |
| `windows_minimize` | 220, 30 | none | 326 ms | `genie` |
| `workspaces` | 300, 34 | none | 266 ms | `slide` |
| `layers_in` | 500, 42 | none | 192 ms | `fade` |
| `layers_out` | 700, 50 | none | 164 ms | `fade` |
| `fade`, `border` | 600, 49 | none | 194 ms | |

`fade_out` shares `windows_out`'s spring, and `borderangle` is off. A fade keeps
its opacity at 1 when a spring passes its target.

The events:

| Event | Parent | Animates | Styles | Reduced motion |
| --- | --- | --- | --- | --- |
| `global` | | Defaults for events with no motion of their own: `ease_out` over 250 ms | | fade |
| `windows` | `global` | Every window animation | | still |
| `windows_move` | `windows` | A window's tile moving or resizing: its surfaces, popups and border are drawn at the tile in between. A change that resizes windows starts once every one of them has drawn its new size, or after 100 ms at most, so a tiling change lands in one frame (see [layout changes land together](./architecture#layout-changes-land-together)) | `morph`: the window stretches ahead of itself toward where it is going, its content filling a liquid blob that melts the rectangle it is at into the one it is about to reach; `elastic`: a window dragged with the mouse bends like jelly, the part under the pointer following it and the rest catching up on a spring (`windows_move`'s, when its curve is a spring) until it settles flat | still |
| `windows_in` | `windows` | A window appearing | `popin <percent>` (the default, `popin 80%`): it grows from that share of its size at its centre; `slide`: it slides in from the nearest edge of its output; `bud`: it grows out of the middle of the nearest window, joined to it by a liquid blob in the inactive border colour that thins as it grows | still |
| `windows_out` | `windows` | A window closing: its last frame plays the same styles in reverse, taking no input | `popin <percent>`, `slide`, `bud`: it flows back into the nearest window | still |
| `windows_minimize` | `windows` | A window minimizing or coming back, in the same motion each way. The window minimized takes no input while it plays; the one coming back is live | `genie` (the default): cut into strips across its way, the window pours down a funnel toward the rectangle the taskbar named with `zwlr_foreign_toplevel_handle_v1.set_rectangle`, or toward the nearest edge of its output when none did, or the one named lies on another output; `fade`: it fades where it is | cross-fade |
| `overview` | `global` | The [overview](#overview) opening and closing, its windows moving to new places in it, and a window dropped on a thumbnail. Unlike the other events its curve does not come from `global`: it is `spring` unless this event names one, because a spring keeps its velocity when the overview reverses or a gesture lets go, and a bézier given here starts each move from rest. `enabled: false` or `animations.enabled: false` jumps, and so does `reduced_motion`. | |
| `fade` | `global` | Every fade | | fade |
| `fade_in` | `fade` | A window's opacity, border and shadow fading in as it appears | | fade |
| `fade_out` | `fade` | A closing window fading out | | fade |
| `fade_switch` | `fade` | A window's opacity changing between `active_opacity` and `inactive_opacity` as focus moves | | fade |
| `fade_dim` | `fade` | `dim_inactive`'s dim coming and going as focus moves | | fade |
| `fade_shadow` | `fade` | A shadow's colour changing between `color` and `color_inactive` | | fade |
| `border` | `global` | A border changing between `borders.active` and `borders.inactive` as focus moves: the new border fades in over the old one | | fade |
| `borderangle` | `global` | A gradient border's angle turning | `once` (the default): one full turn whenever the window gains or loses focus; `loop`: a full turn per `duration` for as long as the window is shown, which keeps its output drawing, but only the border | still |
| `workspaces` | `global` | Switching an output to another workspace: the workspace it leaves stays drawn, taking no input, until the switch ends; layer surfaces and the cursor stay put | `slide` (the default), `slidevert`: both workspaces move along the workspace order, horizontally or vertically; `fade`: they cross-fade; `popin <percent>`: the new one grows from that share of the output and fades in while the old one fades out; `goo`: they cross-fade while each window of the old workspace flows into the nearest window of the new one as a liquid blob in its border colour | cross-fade |
| `layers` | `global` | Every layer-shell surface animation (bars, launchers, notifications) | | fade |
| `layers_in` | `layers` | A layer surface mapping; its exclusive zone applies at once, so windows do not wait for it | `slide` (the default): in from the edge it is anchored to, or the nearest edge; `popin <percent>`; `fade` | cross-fade |
| `layers_out` | `layers` | A layer surface unmapping: its last frame plays out, taking no input | `slide`, `popin <percent>`, `fade` | cross-fade |
| `snap` | `windows` | The [drag preview](#drag_preview) moving to the next place a window would land. It runs on `spring` unless given a curve or a duration | | still |
| `fade_snap` | `fade` | The drag preview appearing and going | | fade |
| `zoom` | `global` | The `zoom` action changing an output's magnification. It runs on `spring` unless given a curve or a duration, in equal ratios, so doubling takes the same motion at any level | | still |

`zoom` magnifies an output around the pointer. The pointer stays where it is on
the screen and the picture moves under it, so the pointer reaches every part of
the output and what it points at is what clients are told. Clients, the layout and
input are untouched, and touches land on what is shown under the finger. Each
output zooms on its own, from `zoom in`, `zoom out`, `zoom reset` and `zoom set` (see
[actions](./actions.md)) on the output under the pointer, up to 16 times. While an
output is zoomed it draws the cursor in the frame, magnified with the rest, in step
with the picture instead of on a cursor plane, and does not scan out directly; both
come back when it returns to 1, at which point it costs nothing. Screenshots and screen
sharing show the output unzoomed, so zooming to read something does not change what
others see or what a recording holds. `lys-wmctl outputs` reports the level as `zoom`
while it is not 1.

Animations run on each output's frame clock: every frame shows the animation
at the time it starts drawing, and an output stops drawing once its
animations end. A spring keeps its velocity when its target changes mid-flight, so a window
retargeted while it moves carries on smoothly. A window being dragged or resized
with the mouse follows the pointer without animating, unless `windows_move` uses
a spring: then the window trails the pointer on that spring and settles where
it is dropped, and a window that comes back with its workspace
is drawn where it belongs at once.

## gestures

Touchpad swipes, pinches and holds can drive lys-wm. A gesture nothing here
claims goes to the window under the pointer, as `zwp_pointer_gestures_v1`.

```yaml
gestures:
  distance: 400
  swipe:
    - {fingers: 3, direction: horizontal, drive: workspaces}
    - {fingers: 4, direction: up, action: exec foot}
  pinch:
    - {fingers: 3, direction: in, action: kill}
    - {fingers: 2, modifiers: SUPER, direction: both, drive: zoom}
  hold:
    - {fingers: 4, action: fullscreen}
```

| Key | Value |
| --- | --- |
| `swipe` | Default: three fingers `horizontal` drive `workspaces` and `vertical` drive `overview`, or `vertical` drive `workspaces` and nothing drives `overview` when `animations.events.workspaces` has the style `slidevert`. Setting the list replaces that default; `swipe: []` turns it off |
| `pinch`, `hold` | Default: none. The built-in config binds SUPER and a two-finger pinch to `drive: zoom` |
| `fingers` | How many; at least 3 for a swipe, since two are scrolling, 2 for a pinch and 1 for a hold |
| `direction` | Swipe: `left`, `right`, `up`, `down`, `horizontal` or `vertical`. Pinch: `in`, `out` or `both`. A hold has none |
| `modifiers` | Keys to hold, as in a chord: `SUPER + CTRL`. A gesture with modifiers is taken only while exactly those are held, ahead of one without, which matches whatever is held. Optional |
| `action` | Anything from [actions.md](actions.md) except the mouse-only `movewindow` and `resizewindow` |
| `drive` | Instead of `action`: something that follows the fingers. `workspaces`, swipes only, takes `horizontal` or `vertical`; `overview`, swipes only, takes `up`, `down` or `vertical`; `zoom`, pinches only, takes `both` |
| `distance` | Unaccelerated touchpad units for one whole swipe, above 0; about 12 units to the millimetre, depending on the touchpad. Default 400 |

Two entries for the same finger count that could match the same movement are an
error. Claiming works the same way for every entry:

- A swipe goes to the client until about a millimetre of travel shows its
  direction. If an entry matches, the client gets a cancelled end and sees
  nothing more of it; otherwise the client keeps the whole gesture.
- A swipe `action` runs once, after 100 units of travel. A pinch claims and runs
  its `action` when the scale passes 15% in that direction. A bound hold never
  reaches the client, and runs its `action` when the fingers lift, unless
  libinput cancels the hold.
- While the session is locked, or a window inhibits shortcuts, every gesture goes
  to the client.

`drive: workspaces` draws the `workspaces` animation of
[animations](#animations) at a position the fingers set, on the output under the
pointer, with both workspaces drawn. It walks the open workspaces of that output
in order, on past a whole workspace, and resists at the first and the last. On
release the position plus 0.15 seconds of the fingers' velocity decides: past
halfway it goes on, otherwise it goes back. A spring then carries that velocity to
rest: the `workspaces` curve when it is a spring, otherwise the built-in `spring`,
since a bézier cannot take a starting velocity. With animations off it jumps.
Three fingers on a settling switch grab it where it is. A workspace switch from
anywhere else ends the gesture, and the rest of its movement is ignored.

`drive: overview` holds the [overview](#overview) at a position the fingers set on
every output together, one `distance` for the whole way, resisting past fully open.
Opening is up on `vertical` and `up`, and along the bound direction on `down`, and
`natural` does not flip it. It takes the input as soon as the swipe is claimed.
On release the position plus 0.15 seconds of velocity decides open or closed from
halfway, and a spring carries that velocity to rest: the `overview` curve when it is
a spring, otherwise the built-in `spring`. With animations off it jumps. Three fingers
on an overview still settling grab it where it is. While it is open, a swipe bound
to `workspaces` does nothing.

`drive: zoom` sets the [zoom](#animations) of the output under the pointer by the
pinch's scale, from the moment the fingers land: the client sees nothing of the
gesture, and the level follows the fingers without animating, from 1 to 16.

The fingers move the content unless `natural_scroll` is `false`, read from
`input.devices` for that device and then `input.touchpad`. Left unset it counts as
on for gestures, whatever libinput's default for scrolling is.

## overview

```yaml
overview:
  blur: true
  dim: 0.35
```

The `overview` action opens every output's overview together: the windows of the
workspace the output shows, side by side and uniformly shrunk so none overlap and
each keeps its shape and its place relative to the others, under a strip of live
thumbnails of the output's workspaces and a `+` tile. `SUPER + Tab` opens it in the
built-in config, and three fingers up scrub it open, [down closed](#gestures).

| Key | Default |
| --- | --- |
| `blur` | `true` on a GPU, `false` on a [slower renderer](#the-default-look): the layers below the windows blur over the mica chain (`decoration.blur.mica`'s size and passes), which redraws only when they change. Independent of `decoration.blur.enabled` |
| `dim` | `0.35`: how far those layers darken, from 0 to 1 |

Everything on the workspace takes part: tiled, floating and fullscreen windows,
every tab of a tab group and windows a fullscreen one covers, on Wayland or X11.
Dialogs stay with their parent as one tile, and popups scale with their window.
Minimized windows sit in a row along the bottom, and a click there brings one back.

Layer surfaces: `background` and `bottom` stay, and blur and dim. `top`, so bars,
fade out as the overview opens and back as it closes, keeping their frame callbacks.
`overlay`, so notifications and launchers, stays on top and takes its own input,
and one with exclusive or clicked keyboard focus keeps the keyboard. X11
override-redirect windows fade out and take no input.

While it is open the overview has the pointer and keyboard. Clients get
`wl_pointer.leave` and `wl_keyboard.leave`, and `enter` with the current keys when
it closes. Binds with modifiers still run.

| Input | Does |
| --- | --- |
| Hover | Rings a window and shows its title, or highlights a thumbnail |
| Click a window | Focuses it, or restores it from the minimized row, and closes |
| Click a thumbnail | Shows that workspace and stays open; on the workspace already shown, closes |
| Click `+` | Shows a new workspace, the lowest number free, and stays open |
| Click anywhere else, `Escape` | Closes |
| Drag a window onto a thumbnail, `+`, or another output | Moves it there with its dialogs; dropped anywhere else it springs back. Minimized windows do not drag |
| Arrow keys, then `Return`, `Space` | Move the ring across windows and the strip on every output, repeating at the seat's rate while held, then act as a click does. Other keys do nothing. Virtual keyboards work the same, through their own keymap |
| Touch | As the pointer, with the first finger |

A session lock closes it at once and it will not open under one, or during a
client's drag and drop.

## tabs

| Key | Default |
| --- | --- |
| `height` | 24 |
| `padding` | 0, between the tab strip and the windows. At 0 the strip and the window read as one rounded shape, with one border and shadow around both; above it the strip rounds on its own |
| `text_height` | 11 |
| `text_padding` | 8 |
| `font` | A TrueType file. Default: `LYS_WM_FONT`, then the DejaVu Sans the package was built with |
| `icon_theme` | The freedesktop icon theme tab icons named by windows resolve in, searched in the XDG data directories. Default: `Adwaita`, which falls back to `hicolor` |
| `active`, `inactive`, `urgent` | `background` and `text` colours, both required. Defaults `#e0947a`/`crust`, `surface0`/`text` and `red`/`crust` |

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
`blur: true` or `blur: false` turns `decoration.blur` on or off for the window and its popups; `blur: false` also ignores the blur regions they ask for through `ext_background_effect_v1`, and turns `mica` off.

`mica: true` or `mica: false` overrides `decoration.blur.mica.enabled` for the window.

`blur_look` overrides the look for the window and its popups, key by key, with the same keys as `decoration.blur`: `tint`, `saturation`, `brightness`, `contrast`, `grain`, `refraction` and `specular`, as `blur_look: {saturation: 1.8, specular: {strength: 0.3}}`.
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
lys-login's lock screen work. While the session is locked, nothing but the lock
surfaces renders or receives input, and only binds flagged `locked` run.
lys-wm tells the locker it is locked only after every output has drawn a
blank frame. If the locker dies, the session stays locked on a dark red
screen until a new locker takes over; outputs added while locked show the
lock colour until the locker covers them. lys-wm does not set logind's
`LockedHint`: the locker does, as lys-login does.

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
