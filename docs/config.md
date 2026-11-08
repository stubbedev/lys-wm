# Config

lys-wm reads one YAML file: `$XDG_CONFIG_HOME/lys-wm/config.yaml`, or
`~/.config/lys-wm/config.yaml` when `XDG_CONFIG_HOME` is unset. Without it,
lys-wm runs the built-in config,
[`internal/config/default.yaml`](https://github.com/stubbedev/lys-wm/blob/main/internal/config/default.yaml),
a good starting point to copy.

Every section is optional. A file replaces the built-in config rather than
adding to it: a key it leaves out takes the default on this page, so a file
without `binds` has no binds and without `env` none of the built-in variables.

Every error names the file, line and key:

```
config.yaml:12: binds[3].action: bad action: unknown action "teleport"
```

| Flag | Effect |
| --- | --- |
| `--config <file>` | Read this file instead; it must exist |
| `--runtime <file>` | Read this override file instead of `runtime.yaml`; it must exist |
| `--check-config` | Load the config and overrides as a reload would, then exit: status 0 and no output when accepted, the errors and status 1 when not |
| `--default-config` | Print the built-in config |
| `--config-schema` | Print the JSON schema, as [`schema/lys-wm-config.schema.json`](https://github.com/stubbedev/lys-wm/blob/main/schema/lys-wm-config.schema.json) |
| `--effective-config` | Print, as JSON, the layers read and every key they set, with its source layer, file, line and value |

`SIGHUP`, the `reload` action and `lys-wmctl reload` read the file again;
editing the file does not. A file that does not parse, or whose chords, keymap
or font do not load, is rejected whole and the running config stays.

An unknown key is a warning: it is skipped and the rest applies. Errors and
warnings show as a red notice on every output until fixed or dismissed
(`dismissconfignotice`). `lys-wmctl reload` prints warnings and exits 1 on a
rejected file; `lys-wmctl configproblems` lists them.

Schema `format`s for string keys:

| Format | Keys | Value | Empty |
| --- | --- | --- | --- |
| `lys-wm-color` | every colour | A palette name or `#rrggbb[aa]` | Refused |
| `lys-wm-font-file` | `tabs.font` | A TrueType or OpenType file; relative to lys-wm's working directory | `LYS_WM_FONT`, then the built-in font |
| `lys-wm-icon-theme` | `tabs.icon_theme` | An installed freedesktop icon theme's directory name; never a path | `Adwaita` |
| `lys-wm-xkb-layout` | `input.keyboard.layout` | Layout names, comma-separated without spaces: `us,dk` | `XKB_DEFAULT_*`, when every keyboard key is empty |
| `lys-wm-xkb-variant` | `input.keyboard.variant` | One variant per layout, at most as many as layouts; an empty item is that layout's default: `,nodeadkeys` | Each layout's default |
| `lys-wm-xkb-options` | `input.keyboard.options` | `group:option` names, comma-separated without spaces | None |
| `lys-wm-xkb-model` | `input.keyboard.model` | One model name: `pc105` | The rules' default |
| `lys-wm-xkb-rules` | `input.keyboard.rules` | One rules name: `evdev` | `evdev` |
| `lys-wm-drm-device` | `gpu` | `cardN` or `/dev/dri/cardN`; never a render node | Refused; leave the key out |
| `lys-wm-chord` | `binds[].chord`, `submaps.*[].chord` | A [chord](#binds-and-submaps) | Refused |
| `lys-wm-action` | every `action` | An [action](./actions.md) | Refused |
| `regex` | `rules[].app_id`, `rules[].title`, `rules[].tag`, `swallow.terminals`, `swallow.except_title` | A Go (RE2) regular expression | Matches everything |

XKB names are those in `/usr/share/X11/xkb/rules/evdev.xml`. A name with a
space or comma, or an empty item in a layout or options list, is an error.

## Runtime overrides

`runtime.yaml` beside the config file is merged over `config.yaml` (or the
built-in config). lys-settings writes it, so `config.yaml` can stay read-only.
lys-wm only reads it; a missing file means no overrides.

- Mappings merge key by key.
- A scalar or list replaces the value below it whole.
- `null` (`gaps:` with nothing after it) clears the whole mapping below it.

```yaml
# runtime.yaml
gaps:
  inner: 8
input:
  keyboard:
    layout: us,dk
```

Messages name the layer's file and line
(`runtime.yaml:3: gaps.inner: -1 is not zero or more`). A bad override rejects
the whole load. `lys-wm --check-config --runtime candidate.yaml` checks a
candidate.

`--effective-config` output. `source` is `runtime`, `config`, or `built-in`
when there is no `config.yaml`; an unlisted key has its default:

```json
{
  "layers": [
    {"source": "config", "file": "/home/me/.config/lys-wm/config.yaml"},
    {"source": "runtime", "file": "/home/me/.config/lys-wm/runtime.yaml"}
  ],
  "keys": [
    {"path": ["gaps", "inner"], "source": "runtime", "file": "/home/me/.config/lys-wm/runtime.yaml", "line": 2, "value": 8}
  ]
}
```

A path is a list of keys (a dotted device name stays one key); a list is one
value. To fold overrides into a Nix config, move every key whose `source` is
`runtime` to the same path, then delete `runtime.yaml`.

## The default look

An empty file looks finished: rounded corners, gaps, a soft shadow, a hairline
border (the focused window's in the accent colour) and spring animations. Any
key you set wins.

Some defaults depend on the renderer chosen at startup:

| Renderer | Default look |
| --- | --- |
| GLES or Vulkan on a GPU | Everything; blur for clients that ask for it |
| GLES on llvmpipe or softpipe, Vulkan on lavapipe or SwiftShader | No blur, the overview's included; `ext_background_effect_v1` is offered without blur |
| Software renderer (`--renderer soft`) | Also no shadow and no animated workspace switch |

Setting `decoration.blur.enabled` or a layer rule's `blur: true` takes blur
back on any renderer; `overview.blur`, `decoration.shadow.enabled` and
`animations.events.workspaces` do the same for theirs.

## outputs

A list of rules. Each output takes the first rule that matches, else the
`match: "*"` rule, else its preferred mode at scale 1. Only one rule may use
`"*"`.

| Key | Value | Default |
| --- | --- | --- |
| `match` | Required. A connector name (`DP-1`), `desc:` plus the start of the EDID make, model and serial (`desc:Samsung Electric Company LC32G5xT`), or `"*"` | |
| `mode` | `preferred`; `highres` (largest, then fastest); `<w>x<h>` (fastest of that size); `<w>x<h>@<hz>` (closest within 1 Hz). A missing mode is reported and the current one kept | `preferred` |
| `position` | `<x>x<y>` logical pixels, or `auto` (right of all others) | `auto` |
| `scale` | 0.25 to 10, in steps of 1/120 | 1 |
| `bitdepth` | `8` or `10`; an output that cannot do 10 stays at 8 and logs it | `8` |
| `vrr` | `off`, `on`, or `fullscreen`: only while one game or video surface (`wp_content_type_v1` or a rule's `content`) alone fills the output | `off` |
| `hdr` | `true`, `false`, or `fullscreen`: only while one surface with an HDR image description alone fills the output, back to SDR a second after. See [HDR](#hdr) | `false` |
| `sdr_brightness` | With `hdr`: cd/m² of SDR white, 1 to 10000 | 203 |
| `hdr_peak` | With `hdr`: display peak in cd/m², 1 to 10000 | EDID, else 400 |
| `icc` | Matrix-shaper ICC profile for an SDR output; absolute, `~/`, or relative to the config file. Runs on the CRTC's colour pipeline where present, else per draw. An unreadable profile is a warning and the output stays sRGB | none |
| `transform` | `normal`, `90`, `180`, `270`, `flipped`, `flipped-90`, `flipped-180`, `flipped-270`. Windows tile in the turned size; absolute pointers, touchscreens and tablets follow; captures stay upright | `normal` |
| `mirror` | Connector or `desc:` of another output to show, letterboxed. A mirror has no workspaces, is hidden from `wl_output` and the layout, and has `mirror_of` in IPC. Black while nothing matches | none |
| `reserved` | `[top, right, bottom, left]` logical pixels kept free of tiled and maximized windows, on top of what bars reserve | none |
| `disabled` | `true` turns the output off | `false` |
| `shader` | Fragment shader for the final image; see [Screen shaders](./shaders). Re-read on reload | none |
| `hot_corners` | `false` turns every [hot corner](#hot_corners) off here; a mapping of corners replaces those corners here | |

Changes from wlr-output-management tools (wlr-randr, kanshi, wdisplays)
replace these rules until the next reload. Rotation, unsupported VRR, unlisted
modes and turning every output off are refused. While the lid is closed,
`eDP-*` panels are off and their workspaces move to other outputs.

### HDR

```yaml
outputs:
  - match: DP-1
    hdr: true
    sdr_brightness: 250
```

An HDR output carries PQ and BT.2020 at 10 bits with the panel's EDID metadata,
overriding `bitdepth`. SDR content shows at `sdr_brightness`, HDR clients at
their own levels, and light above the peak is compressed. The output stays SDR,
and logs why, when the backend is not DRM or headless, the connector lacks
`HDR_OUTPUT_METADATA` or `Colorspace`, the EDID lacks ST 2084 or BT.2020 RGB,
the renderer has no float targets, or the modeset fails.

On an HDR output screen shaders and gamma control are off, only a fullscreen
PQ/BT.2020 buffer matching the panel scans out directly, and translucency
composites in linear light (it looks lighter than on SDR). Captures are always
SDR, HDR content tone-mapped.

`wp_color_manager_v1` is offered when any output has `hdr: true` or
`fullscreen`, or with top-level `color_management: true` (default `false`),
which also tone-maps HDR clients on SDR outputs. Client ICC profiles must be
matrix-shaper with one parametric, gamma or sRGB curve; others fail with
`unsupported`.

## virtual_outputs {#virtual_outputs}

In-memory outputs, created at startup and kept across reloads, for streaming a
second screen (Sunshine, wayvnc, RustDesk) or a screencast at its own size.

```yaml
virtual_outputs:
  - name: tablet
    mode: 2048x1536
```

| Key | Value |
| --- | --- |
| `name` | Required, unique. [`outputs`](#outputs) rules match it like a connector |
| `mode` | Required. `<w>x<h>` or `<w>x<h>@<hz>`; 60 Hz without a rate |

They behave like [`lys-wmctl output create`](./ipc#virtual-outputs) outputs,
rendering only while captured. Reloads add, remove and remake them to match;
`output remove` refuses them; a taken name is reported and skipped. An
[upgrade](./guide/upgrade) recreates them.

## input.keyboard

| Key | Value | Default |
| --- | --- | --- |
| `rules`, `model`, `layout`, `variant`, `options` | XKB names, as in `setxkbmap` | `XKB_DEFAULT_*` when all are unset |
| `repeat_rate` | Repeats per second; 0 turns repeat off | 50 |
| `repeat_delay` | Milliseconds before repeat | 300 |
| `sticky_keys` | A modifier tapped alone applies to the next key; tapped twice it locks until tapped again | `false` |
| `slow_keys` | Milliseconds a key must be held to count; 0 is off | 0 |
| `numlock` | Num Lock on at startup and after every reload | `false` |

`lys-wmctl dispatch stickykeys on|off` and `slowkeys <ms>` change them until
the next reload.

## input.pointer, input.touchpad and input.devices

`input.touchpad` applies to touchpads (devices that can tap), `input.pointer` to
other pointing devices. `input.devices` maps a libinput device name (as
`libinput list-devices` prints it) to settings that override both.

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
| `tap`, `drag_lock` | Bool: tap to click; keep a tap-and-drag after the finger lifts |
| `natural_scroll` | Bool: content follows the fingers; also sets [gesture](#gestures) direction |
| `disable_while_typing` | Bool |
| `middle_emulation` | Bool: left and right together is a middle click |
| `left_handed` | Bool: swap buttons (a tablet: turn it around) |
| `click_method` | `button_areas`, `clickfinger`, `none` |
| `scroll_method` | `two_finger`, `edge`, `on_button_down`, `none` |
| `scroll_button` | Button for `on_button_down`: `left`, `right`, `middle`, `side`, `extra`, `forward`, `back`, `task`, or an evdev code above 0 (`275`) |
| `scroll_button_lock` | Bool: a click on `scroll_button` toggles scrolling |
| `tap_button_map` | `lrm` or `lmr`: buttons for one-, two-, three-finger taps |
| `accel_profile` | `adaptive` or `flat` |
| `accel_points` | `{step, points}`: libinput custom curve, pointer speeds at device speeds 0, `step`, 2·`step`, … `step` above 0 up to 10000; 2 to 64 `points`, each 0 to 10000. Not with `accel_profile` |
| `accel_speed` | -1 to 1 |
| `scroll_factor` | Above 0, up to 100. Default 1 |
| `disabled_on_external_mouse` | Bool: touchpad off while another pointer is plugged in |

Under `input.devices` a device also takes:

| Key | Value |
| --- | --- |
| `output` | For an absolute device (touchscreen, tablet): the output it covers, by connector or `desc:`. While that output is off the device does nothing |
| `calibration` | `[a, b, c, d, e, f]`: libinput calibration matrix on normalized coordinates |
| `region` | `[x, y, width, height]`, fractions of a tablet's surface, stretched over its output |
| `keyboard` | `rules`, `model`, `layout`, `variant`, `options` for this keyboard alone, replacing the seat's names whole (an unset key takes the XKB default) |

An unset key keeps libinput's default. A setting a device lacks is skipped, or
reported when named under `devices`. Applies on the drm backend only.

## cursor

| Key | Value | Default |
| --- | --- | --- |
| `hide_while_typing` | Hide the pointer when a key that is neither a bind nor a lone modifier reaches a window | `false` |
| `hide_after` | Milliseconds without pointer input before hiding; 0 never. Never during a button hold, move, resize, drag or pointer lock | 0 |
| `shake_to_find` | Shaking the mouse grows the pointer to three times its size briefly | `false` |

Any pointer, touch or pen event shows it again. Pointer size comes from
`XCURSOR_SIZE`.

## Colours

A colour is a palette name or `#rrggbb[aa]`. The palette is
[Catppuccin Mocha](https://catppuccin.com/palette) (`mauve`, `crust`,
`surface0`, …) plus `accent`. `palette` adds or overrides names, with
`#rrggbb[aa]` values:

```yaml
palette:
  accent: "#ff8800"
```

`accent` follows the `accent-color` xdg-desktop-portal publishes (in a
lys-shell session, `styling.palette.primary`) and reloads the config live when
it changes; until one is published it is `#e0947a`. Setting `accent` under
`palette` pins it.

## borders

| Key | Value | Default |
| --- | --- | --- |
| `width` | Logical pixels, 0 or more | 1 |
| `active` | Colour or gradient | `accent` |
| `inactive` | Colour or gradient | `#ffffff14` |

A gradient is 2 to 10 colours along `angle` degrees (0, the default, is left to
right; clockwise):

```yaml
borders:
  active: {colors: [mauve, blue], angle: 45}
```

## decoration

| Key | Value | Default |
| --- | --- | --- |
| `rounding` | Corner radius in logical pixels, 0 or more. Fullscreen windows stay square | 8 |
| `active_opacity` | 0 to 1 | 1 |
| `inactive_opacity` | 0 to 1 | 1 |
| `fullscreen_opacity` | 0 to 1 | 1 |
| `dim_inactive` | Darken every window but the focused one | `false` |
| `dim_strength` | 0 to 1, for `dim_inactive` and `dim_around` | 0.5 |

Opacity multiplies what the client draws; borders stay opaque. A rule's
`opacity` replaces these. Rounded or translucent windows are never scanned out
directly.

### decoration.shadow

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | Shadow under every non-fullscreen window (see [the default look](#the-default-look)) | `true` |
| `range` | Fade distance in logical pixels, 0 or more | 24 |
| `render_power` | Fade speed, 1 (linear) to 4 | 2 |
| `sharp` | Hard edge, no fade | `false` |
| `offset` | `[x, y]` logical pixels | `[0, 8]` |
| `scale` | Size relative to the window, above 0 up to 1 | 1 |
| `color` | Colour | `#00000060` |
| `color_inactive` | Colour for unfocused windows | `#00000038`, or `color` when set |

### decoration.blur

Blurs what lies behind translucent windows, layer surfaces whose layer rule
sets `blur`, and their popups.

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | Blur behind every translucent window and its popups | `false` |
| `size` | Distance between taps, logical pixels, 1 or more | 6 |
| `passes` | 1 to 4; each doubles the reach | 2 |
| `xray` | Blur only the `background` and `bottom` layers, one shared blur per output | `false` |
| `live_tiled` | `true` blurs what is behind each tiled window; `false` gives tiled windows the shared background blur | `false` |
| `tint` | Colour over the blurred backdrop | none |
| `saturation` | 0 to 4, CSS `saturate()` | 1.3 |
| `brightness` | 0 to 4, CSS `brightness()` | 1 |
| `contrast` | 0 to 4, CSS `contrast()` | 1 |
| `grain` | Luminance noise, 0 to 1 | 0 |
| `refraction.bezel` | Logical pixels in from the edge where the backdrop bends, 0 or more | 16 when `refraction` is set |
| `refraction.amount` | Logical pixels it looks past the edge, 0 or more | 8 when `refraction` is set |
| `specular.strength` | Rim sheen brightness, 0 to 1 | 0.6 when `specular` is set |
| `specular.width` | Rim width, logical pixels, 1 or more | 1 when `specular` is set |
| `mica.enabled` | Draw translucent window parts over a heavy blur of the `background` and `bottom` layers, shared per output | `false` |
| `mica.size` | As `size` | 16 |
| `mica.passes` | As `passes` | 4 |
| `mica.tint` | Replaces `tint` for mica windows | none |

Saturation, brightness and contrast apply in that order, then tint, then
grain. The sheen faces the pointer; with reduced motion or animations off it
rests at the top left.

Client blur regions (`ext_background_effect_v1`) are honoured whether or not
`enabled` is set, using this section's settings, and replace whole-surface
blur. A CPU renderer withholds them unless `enabled` or a layer rule's `blur` is
set. A window rule's `blur: false` turns them off for that window.

## drag_preview {#drag_preview}

While a window is dragged with `movewindow`, a translucent rectangle bordered in
`borders.active` shows where it will land. A tiled window lands at its place in
the tree. A floating window lands under the pointer, or snaps when the pointer
is within `snap_distance` of a usable-area edge not shared with another output:

| Pointer near | Window lands on |
| --- | --- |
| Left or right edge | That half |
| Bottom edge | Bottom half |
| Top edge | Whole usable area |
| A corner | That quarter |

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | Off: no preview and no snapping | `true` |
| `opacity` | Fill opacity, 0 to 1 | 0.25 |
| `blur` | Blur behind the rectangle with `decoration.blur` | `false` |
| `snap_distance` | Logical pixels, 1 or more | 16 |
| `snap_windows` | Floating windows also snap beside (`gaps.inner` apart) or aligned with other floating windows | `false` |
| `drag_switch.width` | Logical pixels from an output edge with no neighbour, 1 or more | 4 |
| `drag_switch.frames` | Frames a drag must be held there, 1 or more | 30 |

A drag held within `drag_switch.width` of such an edge for
`drag_switch.frames` switches to the previous or next workspace (vertically for
`slidevert`), taking a dragged window along. Holding a client drag over a
background tab raises it.

## floating

| Key | Value | Default |
| --- | --- | --- |
| `step` | Logical pixels `move <dir>` moves a floating window, 1 or more | 40 |

At the usable area's edge the window crosses to the next output. For `place`
see [actions](./actions.md).

## gaps

| Key | Value | Default |
| --- | --- | --- |
| `inner` | Logical pixels between tiled windows, 0 or more | 8 |
| `outer` | Logical pixels to the usable area's edges: one number or `[top, right, bottom, left]`, 0 or more | 8 |

Floating and fullscreen windows ignore gaps.

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

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | Bool | `true` |
| `reduced_motion` | Bool: events marked **still** below happen at once; **fade** and **cross-fade** become plain `ease_out` fades. Reported by `lys-wmctl reducedmotion` and IPC; lys-shell follows it and publishes it to apps through the portal | `false` |
| `curves` | Named curves: `bezier: [x1, y1, x2, y2]` (x in 0 to 1) or `spring: {stiffness, damping, mass}` (all above 0, `mass` default 1) | |
| `events` | Per event: `enabled`, `curve`, `duration` (milliseconds, above 0; bézier only), `style` | |

Built-in curves, not redefinable: `linear`, `ease`, `ease_in`, `ease_out`,
`ease_in_out` (CSS) and `spring` (stiffness 170, damping 26).

An event's unset key comes from its parent, else its default motion below. A
`duration` with no curve runs `ease_out`. An unknown event is a warning.

| Event | Default motion | Default style |
| --- | --- | --- |
| `windows_move` | spring 800, 54 | |
| `windows_in` | spring 700, 40 | `popin 92%` |
| `windows_out` | spring 900, 56 | `popin 94%` |
| `windows_minimize` | spring 220, 30 | `genie` |
| `workspaces` | spring 900, 60 | `slide` |
| `scratchpad` | spring 900, 60 | `slidevert` |
| `overview` | spring 300, 34 (a spring unless this event names a curve) | |
| `layers_in` | spring 800, 54 | `fade` |
| `layers_out` | spring 900, 58 | `fade` |
| `fade` | spring 800, 56 | |
| `fade_out` | spring 900, 56 | |
| `snap`, `zoom` | `spring` unless given a curve or duration | |
| `borderangle` | off | `once` |
| others | inherited; `global` is `ease_out` over 250 ms | |

| Event | Parent | Animates | Styles | Reduced motion |
| --- | --- | --- | --- | --- |
| `global` | | Defaults for all | | fade |
| `windows` | `global` | Every window animation | | still |
| `windows_move` | `windows` | A tile moving or resizing; a resize waits until every affected window has drawn its new size, up to 100 ms ([layout changes land together](./architecture#layout-changes-land-together)) | `morph` (stretches toward its target as a liquid blob), `elastic` (a mouse-dragged window bends like jelly) | still |
| `windows_in` | `windows` | A window appearing | `popin <percent>`, `slide`, `bud` (grows out of the nearest window) | still |
| `windows_out` | `windows` | A window closing | `popin <percent>`, `slide`, `bud` | still |
| `windows_minimize` | `windows` | Minimizing and restoring | `genie` (toward the taskbar's rectangle, else the nearest edge), `fade` | cross-fade |
| `overview` | `global` | The [overview](#overview) opening, closing and rearranging | | still |
| `fade` | `global` | Every fade | | fade |
| `fade_in` | `fade` | A window or the session lock fading in | | fade |
| `fade_out` | `fade` | A window or the session lock fading out | | fade |
| `fade_switch` | `fade` | Active/inactive opacity change | | fade |
| `fade_dim` | `fade` | `dim_inactive` coming and going | | fade |
| `fade_shadow` | `fade` | Shadow `color`/`color_inactive` change | | fade |
| `borderangle` | `global` | A gradient border's angle | `once` (one turn on focus change), `loop` (one turn per `duration`, continuously) | still |
| `workspaces` | `global` | Switching an output's workspace | `slide`, `slidevert`, `fade`, `popin <percent>`, `goo` | cross-fade |
| `scratchpad` | `global` | A [scratchpad](#scratchpads) showing or hiding | `slidevert`, `fade`, `popin <percent>` | cross-fade |
| `layers` | `global` | Every layer-shell surface animation | | fade |
| `layers_in` | `layers` | A layer surface mapping | `slide`, `popin <percent>`, `fade` | cross-fade |
| `layers_out` | `layers` | A layer surface unmapping | `slide`, `popin <percent>`, `fade` | cross-fade |
| `snap` | `windows` | The [drag preview](#drag_preview) moving | | still |
| `fade_snap` | `fade` | The drag preview appearing and going | | fade |
| `zoom` | `global` | The `zoom` action changing magnification | | still |

A spring keeps its velocity when retargeted. A mouse-dragged window follows the
pointer directly, or trails it when `windows_move` is a spring.

The `zoom` [action](./actions.md) magnifies an output around the pointer, up to
16×, without touching clients, layout or input. Captures show it unzoomed.

## gestures

Touchpad swipes, pinches and holds, and [touchscreen](#touchscreens) swipes. A
gesture nothing here claims goes to the window under the pointer.

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

| Key | Value | Default |
| --- | --- | --- |
| `swipe` | List of entries. Setting it replaces the default; `[]` turns swipes off | 3 fingers `horizontal` drive `workspaces`, `vertical` drive `overview`; with `workspaces` style `slidevert`, only `vertical` drive `workspaces` |
| `pinch` | List of entries | none |
| `hold` | List of entries | none |
| `distance` | Unaccelerated touchpad units for one whole swipe (about 12 per mm), above 0 | 400 |

Each entry:

| Key | Value |
| --- | --- |
| `fingers` | Required. Swipe at least 3, pinch at least 2, hold at least 1 |
| `direction` | Required for swipes and pinches, not allowed for holds. Swipe: `left`, `right`, `up`, `down`, `horizontal`, `vertical`, `any` (only with `move`/`resize`). Pinch: `in`, `out`, `both` |
| `modifiers` | Optional, as in a chord (`SUPER + CTRL`). With modifiers, fires only with exactly those held, ahead of an entry without |
| `action` | Any [action](./actions.md) except `movewindow` and `resizewindow` |
| `drive` | Instead of `action`, follow the fingers: `workspaces` (swipe, `horizontal`/`vertical`), `overview` (swipe, `up`/`down`/`vertical`), `view` (swipe, `horizontal`), `move` and `resize` (swipe, `any`), `zoom` (pinch, `both`) |
| `scope` | Only with `drive: overview`: `workspace` (default), `output` or `app` |

Two entries for the same finger count that can match the same movement are an
error. A swipe is claimed once its direction shows (the client gets a cancel)
and its `action` runs after 100 units; a pinch at 15% scale; a hold when the
fingers lift. While locked or shortcuts are inhibited, gestures go to the
client.

- `workspaces`: scrubs through the output's workspaces; on release, position
  plus velocity decides past halfway, settling on a spring.
- `overview`: scrubs the overview open (up for `up`/`vertical`, down for
  `down`) on the entry's `scope`.
- `view`: scrolls a [scrolling workspace](#layout-and-scroll), one `distance`
  per output width, settling on a column edge.
- `zoom`: zoom follows the pinch, 1 to 16.
- `move`, `resize`: as `movewindow`/`resizewindow` with the fingers.

Swipes move the content unless `natural_scroll` is `false` for the device
(`input.devices`, then `input.touchpad`); unset counts as on.

### Touchscreens

```yaml
gestures:
  edge_width: 16
  touch:
    - {edge: bottom, drive: overview}
    - {fingers: 3, direction: horizontal, drive: workspaces}
    - {fingers: 3, direction: any, drive: move}
```

| Key | Value |
| --- | --- |
| `edge_width` | Pixels from an output edge where an `edge` swipe starts, 1 or more. Default 16 |
| `touch[].edge` | `top`, `bottom`, `left`, `right`: one finger swiping in from that edge; no `direction` |
| `touch[].fingers` | Instead of `edge`: 3 to 10 fingers anywhere, with a `direction` as for touchpad swipes |
| `touch[].action`, `touch[].drive` | As for touchpad swipes; no `zoom` |

Touches reach the window at once; after about 12 pixels in a bound direction
lys-wm claims them (`wl_touch.cancel`). Drives use the output's size as the
whole way.

## hot_corners {#hot_corners}

```yaml
hot_corners:
  top_left: {action: overview toggle}
  bottom_right: {action: global dev.stubbe.lys.shell:dashboard, modifiers: SUPER}
  pressure: 80
  fullscreen: false
```

| Key | Value | Default |
| --- | --- | --- |
| `top_left`, `top_right`, `bottom_left`, `bottom_right` | `{action, modifiers}`: any [action](./actions.md) but `movewindow`/`resizewindow`; optional `modifiers` must be held. `false`: off | off |
| `pressure` | Unaccelerated pointer units of push into the corner before it fires, above 0 | 80 |
| `fullscreen` | Fire while a fullscreen window has focus | `false` |

A corner re-arms when the pointer leaves. Corners bordering another output
never fire, nor while the pointer is confined or locked or the session is
locked. [`outputs[].hot_corners`](#outputs) overrides them per output.

## overview

| Key | Value | Default |
| --- | --- | --- |
| `blur` | Blur the layers below the windows, independent of `decoration.blur.enabled` | `true` on a GPU, `false` on a [slower renderer](#the-default-look) |
| `dim` | How far those layers darken, 0 to 1 | 0.35 |

The `overview` action shows every output's windows shrunk side by side without
overlap, under a strip of live workspace thumbnails and a `+` tile. Built-in
bind: `SUPER + Tab`.

`overview [toggle|show|hide] [workspace|output|app]`:

| Scope | Shows |
| --- | --- |
| `workspace` | Default. The shown workspace's windows |
| `output` | Every workspace of each output, grouped and labelled; dropping a window on another group moves it there |
| `app` | Windows sharing the focused window's `app_id`, from everywhere, on the focused output |

Another scope switches an open overview; `toggle` with its current scope
closes it. Typing filters by title and `app_id`, case-insensitively. All
windows take part, dialogs with their parent, minimized ones in a row at the
bottom. `top` layers fade out; `overlay` stays on top. While open it has
pointer and keyboard; binds with modifiers still run.

| Input | Does |
| --- | --- |
| Hover | Rings a window and shows its title, or highlights a thumbnail |
| Click a window | Focuses (or restores) it and closes |
| Click a thumbnail | Shows that workspace; on the shown one, closes |
| Click `+` | Shows the lowest free workspace number |
| Click elsewhere, `Escape` | Closes; `Escape` first clears a filter |
| Drag a window onto a thumbnail, `+` or another output | Moves it there with its dialogs |
| Arrow keys, then `Return` or `Alt + Space` | Move the ring, then act as a click |
| `Down` with nothing below, `Up` with nothing above | Switch to the `app` scope for the ringed window, and back |
| Printable keys, `BackSpace` | Edit the filter |
| `Tab`, `Shift + Tab` | Next or previous window, wrapping |
| `Alt + 1` … `Alt + 9`, `Alt + 0` | Show that workspace (`0` is 10) |
| `Delete`, `Alt + x` | Close the ringed window |
| `Shift + Left`, `Shift + Right` | Move the ringed window to the workspace one below or above |
| Touch | As the pointer, first finger |

A session lock closes it. During a client drag, holding over a thumbnail or
window for `drag_preview.drag_switch.frames` shows or focuses it.

## switcher

| Key | Value | Default |
| --- | --- | --- |
| `open_delay` | Milliseconds the modifiers must be held before the preview strip shows, 0 or more | 150 |

The `switcher` action is Alt-Tab over windows in most-recently-used order
across all workspaces and outputs (`switcher next app` keeps to the focused
app). Releasing the modifiers focuses the selection; `Escape` cancels. A quick
tap flips to the previous window without drawing anything. Focus is recorded
once it settles (a bind's at key release, hover's at the next input).
`focuslast`, `focusurgent` and `cycle` walk the same order.

## tabs

| Key | Value | Default |
| --- | --- | --- |
| `height` | Logical pixels, 1 or more | 24 |
| `padding` | Between strip and windows, 0 or more. At 0 strip and window share one outline | 0 |
| `text_height` | 1 or more | 11 |
| `text_padding` | 0 or more | 8 |
| `font` | A TrueType or OpenType file | `LYS_WM_FONT`, then the packaged DejaVu Sans |
| `icon_theme` | Freedesktop icon theme name | `Adwaita` (then `hicolor`) |
| `active`, `inactive`, `urgent` | `{background, text}`, both required | `accent`/`crust`, `surface0`/`text`, `red`/`crust` |

Tabs at least four icons wide show the window's `xdg_toplevel_icon_v1` icon.

## titlebars

A title bar on floating windows that let lys-wm decorate them
(`xdg-decoration`, KDE server decoration, X11 `_MOTIF_WM_HINTS`): the window
buttons as coloured dots, the title centred, in the `tabs` font. The defaults
are lys-shell's default surface, text and status colours; they do not follow
its theme. Hovering a button shows the glyphs on all of them. An unfocused
window's dots take the `inactive` text colour.

| Key | Value | Default |
| --- | --- | --- |
| `floating` | `false` turns bars off | `true` |
| `height` | Logical pixels, taken from the floating rect, 1 or more | 24 |
| `buttons` | Any of `minimize`, `maximize`, `close`, in order, each once | `close`, `minimize`, `maximize` |
| `side` | `right` or `left` | `left` |
| `double_click` | `maximize` toggles maximize; `float` tiles the window | `maximize` |
| `active`, `inactive`, `urgent` | `{background, text}`, both required | `#1c1c2c`/`#d4d6e8`, `#1c1c2c`/`#8a8ca4`, `red`/`crust` |
| `lights.close`, `lights.minimize`, `lights.maximize` | Colour of that button's dot | `#e46870`, `#e0b870`, `#68c898` |

Dragging the bar moves the window. Middle click minimizes; right click sends
the `windowmenu` [IPC event](./ipc#events).

## layout and scroll

```yaml
layout: tree
scroll:
  presets: [0.333, 0.5, 0.667, 1280px]
  center: never
```

| Key | Value | Default |
| --- | --- | --- |
| `layout` | `tree` (splits and tab groups) or `scroll` (a strip of columns): a new workspace's layout. A workspace rule's `layout` wins; `setlayout` converts | `tree` |
| `scroll.presets` | Non-empty list of widths `columnwidth` cycles: shares above 0 up to 1, or pixels (`1280px`) | `[1/3, 0.5, 2/3]` |
| `scroll.center` | When the view scrolls to the focused column: `never` (just enough to show it), `always`, `on-overflow` (when not wholly shown) | `never` |

In `scroll`, a new window opens as a column right of the focused one, half the
output wide. A column holds one window, a stack or tabs (`consume`, `expel`,
`columndisplay`). Off-screen columns draw nothing and get frame callbacks at
[`hidden_frame_rate`](#hidden_frame_rate).

## autotile

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | Bool | `true` |
| `ephemeral` | Groups autotile creates dissolve at one window | `true` |
| `min_trigger` | Pixels, 0 or more | 400 |
| `divisor` | 1 or more | 3 |

When one more column would make columns narrower than the larger of
`min_trigger` and output width / `divisor`, the new window stacks under the
focused one.

## binds and submaps

```yaml
binds:
  - chord: SUPER + SHIFT + Q
    action: kill
  - {chord: SUPER + R, action: submap resize}
  - {chord: SUPER, action: global shell:launcher, description: Open the launcher}
  - {chord: SUPER + L, action: exec lock, long_press: 600}
  - {chord: XF86AudioPlay, action: exec playerctl play-pause, flags: [pass]}
  - {chord: SUPER + mouse_down, action: workspace e+1, cooldown: 150}
submaps:
  resize:
    - {chord: Right, action: resizeactive 20 0, flags: [repeating]}
    - {chord: Escape, action: submap reset}
```

| Key | Value |
| --- | --- |
| `chord` | Required. Modifiers and a trigger joined by `+` |
| `action` | Required. See [actions](./actions.md). `submap <name>` must name a defined submap |
| `flags` | List of flags, below |
| `long_press` | Milliseconds, 1 or more: fire on release only if held that long |
| `cooldown` | Milliseconds, 1 or more: ignore the bind within that time of its last firing |
| `description` | Text listed by `lys-wmctl binds` and the IPC `binds` query |

Modifiers: `SUPER` (`MOD4`, `WIN`, `LOGO`), `SHIFT`, `CTRL` (`CONTROL`), `ALT`
(`MOD1`). Triggers: a keysym from the first layout (`Return`, `Q`,
`XF86AudioRaiseVolume`), `mouse:<code>` (`mouse:272` is left), or
`mouse_up`/`mouse_down` for the wheel. A modifier alone (`SUPER`, `SUPER_L`,
`ALT_R`, …) fires on release when nothing else was pressed in between, and
still reaches the window. Two binds with the same chord are an error.

| Flag | Effect |
| --- | --- |
| `locked` | Works while the session is locked |
| `repeating` | Repeats at the keyboard repeat rate while held |
| `release` | Fires on release, only if nothing else was pressed in between |
| `pass` | The focused window gets the input too |
| `inhibit_proof` | Runs while a window inhibits shortcuts |

A bind that fires on release cannot repeat. While a window inhibits shortcuts
(VMs, remote desktops) only `locked` and `inhibit_proof` binds run.

## client_hotkeys {#client_hotkeys}

```yaml
client_hotkeys: true
```

Default `false`. Lets applications bind global hotkeys through the experimental
`xx_hotkey_v1` protocol; the focused window does not see them. Allowed
triggers: anything with `CTRL`, `ALT` or `SUPER`; otherwise only keys that type
no text or auxiliary mouse buttons. Triggers used by `binds` or another
application are refused. Nothing fires while locked or inhibited. Sandboxed
applications do not see the protocol.

## focus

| Key | Value | Default |
| --- | --- | --- |
| `follows_mouse` | `always`: hover focuses; `loose`: as `always`, but after focus moves another way the pointer must enter a different window first; `off`: click to focus | `loose` |
| `raise_on_hover` | Hover focus also raises a floating window | `false` |
| `warp_pointer` | `off`; `on_keyboard_focus`: centre the pointer on a window a key bind focuses; `on_workspace_switch`: only when a key bind changes the shown workspace or output | `off` |
| `on_close` | After the focused window closes, focus `next` (the previously focused) or `under_pointer` | `next` |
| `on_activate` | `focus` or `urgent`: what a valid `xdg_activation_v1` request does | `focus` |

An activation token is valid when it comes from input in the focused client and
focus has not moved since; other activations mark the window urgent. `exec`,
`autostart` and `dispatch exec` pass a token (`XDG_ACTIVATION_TOKEN`,
`DESKTOP_STARTUP_ID`); a window presenting it before mapping opens on the
workspace shown at launch.

## fullscreen

| Key | Value | Default |
| --- | --- | --- |
| `new_window` | A window opening where another is fullscreen or maximized: `behind` (the other keeps focus), `take_over` (gets the state), `exit` (state ends, new window focused) | `behind` |

`fullscreen on fake` tells a window it is fullscreen while it stays in its
tile.

## layer_rules {#layer_rules}

```yaml
layer_rules:
  - {namespace: ^lys-shell-notification-popup$, block_out_from: screencast}
```

Rules for layer-shell surfaces, decided once per surface; later rules override
earlier ones.

| Key | Value |
| --- | --- |
| `namespace` | Required. Regular expression on the namespace |
| `blur` | `true`: blur behind the surface and its popups with `decoration.blur` |
| `blur_alpha_threshold` | 0 to 1: blur only under texels with alpha above it |
| `animation` | `false`, or `{in: <style>, out: <style>}` with `layers_in`/`layers_out` styles |
| `shadow` | `true`: shadow as `decoration.shadow` |
| `rounding` | Corner radius in pixels, 0 or more |
| `opacity` | 0 to 1 |
| `dim_around` | `true`: darken everything below by `decoration.dim_strength` |
| `block_out_from` | `screencast` or `capture`, as for windows |

Only captures declared one-shot through `lys_capture_hint_v1` count as
screenshots; every other capture is a screencast.

## workspaces

```yaml
workspaces:
  - {id: 1, output: "desc:LG Electronics 27GL850", default: true, persistent: true}
  - {id: 9, name: chat, output: eDP-1, on_created_empty: vesktop}
  - {match: single_tiled, gaps: {inner: 0, outer: 0}, borders: {width: 0}, rounding: 0}
```

A rule has exactly one of `id` (a workspace number, 1 or more) or `match`
(`single_tiled`, `fullscreen` or `any`). `match` rules apply after `id` rules,
in order, and take only the look keys.

| Key | Value |
| --- | --- |
| `output` | Connector or `desc:`: the workspace is created there and returns there |
| `default` | `true`: the output shows it first. Needs `output` |
| `persistent` | `true`: exists from startup and is never dropped |
| `name` | Unique; a letter, then letters, digits, `-` or `_`; not `previous` or `empty`. Accepted by `workspace` and `movetoworkspace` |
| `on_created_empty` | Command run each time the workspace is shown empty |
| `layout` | `tree` or `scroll` |
| `gaps`, `borders.width`, `rounding`, `shadow` | Look instead of `gaps`, `borders.width`, `decoration.rounding`, `decoration.shadow.enabled`; window rules still win |

## swallow

```yaml
swallow:
  terminals: ^(foot|kitty|Alacritty|org\.wezfurlong\.wezterm)$
  except_title: wev|lazygit
```

| Key | Value |
| --- | --- |
| `terminals` | Regex on `app_id`. Without it nothing swallows |
| `except_title` | Regex on the new window's title: these never swallow |

A window whose process descends (up to eight levels) from a matching terminal
takes its place; the terminal hides until the window closes. Floating windows,
dialogs, rule-placed windows, sandboxed or other-pid-namespace clients and
floating terminals never swallow. `swallow off|on` toggles it.

## scratchpads

```yaml
scratchpads:
  term: {exec: foot --app-id dropterm, size: 60%x50%}
  music: {exec: spotify, hide_on_workspace_switch: false}
```

| Key | Value | Default |
| --- | --- | --- |
| `exec` | Command run when toggled while empty; its window lands in the scratchpad | none |
| `size` | `<w>%x<h>%`, each 1% to 100% of the usable area, centred | whole area |
| `hide_on_workspace_switch` | Hide when the output switches workspace | `true` |

`scratchpad toggle <name>` shows a named set of windows over the focused
output, dimming the workspace by `decoration.dim_strength`.
`movetoscratchpad <name>` adds a window, `movetoworkspace` removes it. A scratchpad needs no entry
here. ext-workspace lists it as hidden workspace `scratchpad:<name>`.

## showdesktop

| Key | Value | Default |
| --- | --- | --- |
| `peek` | Percent of the output's width left visible of each window, 1 to 50 | 2 |

`showdesktop` slides the output's windows (every output's with `all`) to their
nearest edges. A click on a sliver, a new window, a workspace switch or the
action again brings them back.

## rules

```yaml
rules:
  - {app_id: ^steam$, float: true}
  - {app_id: ^steam$, title: ^Friends List$, size: 400x900}
  - {app_id: ^(discord|spotify)$, workspace: 9 silent, output: eDP-1}
  - {app_id: ^keepassxc$, title: Unlock, focus: false, position: center}
  - {app_id: ^steam_app_, fullscreen: false, suppress: [fullscreen]}
  - {initial_title: ^Picture-in-Picture$, float: true, size: 25%x25%, position: 74%x72%, pin: true}
```

Later rules override earlier ones. A rule needs at least one matcher.
`lys-wmctl dispatch setprop <id> <key> <value>` applies a rule key to one
window until it closes (`setprop 7 border {width: 4}`).

| Matcher | Matches |
| --- | --- |
| `app_id`, `title` | [RE2](https://github.com/google/re2/wiki/Syntax) regex. For X11, `app_id` is the `WM_CLASS` class |
| `tag` | Regex on the `xdg_toplevel_tag_v1` tag; the empty string without one |
| `initial_app_id`, `initial_title` | Regex on the values at first map |
| `xwayland` | `true` X11, `false` Wayland |
| `is_floating`, `is_focused`, `is_fullscreen`, `is_urgent`, `is_captured` | Bool state, re-checked when it changes |
| `exclude` | A non-empty mapping of matchers; matching windows are dropped |

Placement, applied once at first map:

| Key | Value |
| --- | --- |
| `float`, `tile` | `true` (not both): where a new window goes |
| `workspace` | `<n>` (open there and follow) or `<n> silent` |
| `output` | Connector or `desc:`: open on its shown workspace |
| `focus` | `false`: open without focus or urgency |
| `fullscreen`, `maximized` | `true` opens so; `false` refuses the request before map |
| `position` | Floating: `center`, `cursor`, or `<x>x<y>` in pixels or percent of the usable area |
| `size` | `<w>x<h>` in pixels or percent of the usable area, above 0 |
| `group` | `tab`, `h` or `v`: open inside the focused window's group of that layout |
| `suppress` | List of `fullscreen`, `maximize`, `activate`: always ignore those requests |
| `pin` | `true`: pinned to every workspace of its output, floating |

A window launched by `exec` opens on its launching workspace unless `workspace`
or `output` says otherwise.

Properties, held while the rule matches:

| Key | Value |
| --- | --- |
| `border` | `{active, inactive, width}`, each optional, as in `borders` |
| `rounding` | Pixels, 0 or more |
| `shadow` | Bool |
| `dim_inactive` | Bool |
| `dim_around` | `true`: darken everything below by `decoration.dim_strength` |
| `opacity` | 0 to 1, focused or not |
| `blur` | Bool, for the window and its popups; `false` also ignores client blur regions and mica |
| `mica` | Bool |
| `blur_look` | `decoration.blur` look keys: `tint`, `saturation`, `brightness`, `contrast`, `grain`, `refraction`, `specular` |
| `animation` | `false`, or `{in: <style>, out: <style>}` with `windows_in`/`windows_out` styles |
| `idle_inhibit` | `visible`, `focus` or `fullscreen`: block idle while the window is shown, focused, or fullscreen |
| `vrr` | `true`: adaptive sync on its output while shown |
| `tearing` | Bool, overriding `wp_tearing_control_v1` (see [tearing](#tearing)) |
| `content` | `none`, `photo`, `video`, `game`, overriding `wp_content_type_v1` (affects `vrr: fullscreen` and tearing) |
| `on_activate` | `focus`, `urgent` or `ignore`, overriding [`focus.on_activate`](#focus) |
| `scroll_factor` | Above 0, up to 100 |
| `min_size`, `max_size` | `<w>x<h>` pixels, above 0 |
| `keep_aspect` | Bool: resizing a floating window keeps its aspect |
| `touch_gestures` | `false`: no [touch gesture](#touchscreens) claims touches starting on it |
| `block_out_from` | `screencast`: black in screencasts but not screenshots; `capture`: black in every capture |

Without a rule a window floats when it has a parent or a fixed size, is an
`xdg_dialog_v1`, or is an X11 modal or dialog-type window.

## env and autostart

```yaml
env:
  QT_QPA_PLATFORM: wayland;xcb
autostart:
  - waybar
```

`env` is set in lys-wm's environment at startup, so everything it runs
inherits it. `XDG_CURRENT_DESKTOP=lys-wm` and `XDG_SESSION_TYPE=wayland` are set
unless overridden. `WAYLAND_DISPLAY`, `DISPLAY` and `LYS_WM_SOCKET` are
refused. `XCURSOR_*` also sets lys-wm's own pointer.

Each `autostart` command runs once with `sh -c` once the display and IPC socket
are up. Reloads change neither key. Failures are logged.

## xwayland

| Key | Value | Default |
| --- | --- | --- |
| `enabled` | `false`: no X sockets, `DISPLAY` unset | `true` |
| `zero_scaling` | X programs see outputs at physical resolution, scale 1 (sharp, small on scaled outputs). `false`: X windows are stretched (right size, blurry) | `true` |

Xwayland starts on the first X client. Both keys are read at startup only.

## tearing

```yaml
tearing: true
rules:
  - {app_id: ^cs2$, tearing: true}
```

Default `false`. When `true`, a window that fills an output and scans out
directly uses async page flips if a rule says `tearing: true`, it asks via
`wp_tearing_control_v1`, or it states nothing and is a game. A rule with
`tearing: false`, or a vsync hint without a rule, keeps vblank. Needs atomic
async flips (Linux 6.8 or later).

## hidden_frame_rate {#hidden_frame_rate}

```yaml
hidden_frame_rate: 5
```

Default 15, a positive integer: frame callbacks per second for a surface fully
covered by opaque windows or on no output.

## power_saver_frame_rate {#power_saver_frame_rate}

```yaml
power_saver_frame_rate: 0
```

Default 60, an integer 0 or more; 0 is off. While power-profiles-daemon's
profile is `power-saver`, a faster output composites every k-th vblank (k is
refresh / rate rounded up) and its surfaces get frame callbacks at that rate.
Under adaptive sync it is a minimum frame interval.

## hold_opening {#hold_opening}

```yaml
hold_opening: true
```

Default `false`: a new window shows at once and resized neighbours are
stretched until they redraw. `true` waits up to 100 ms for them first.

## lid_suspend {#lid_suspend}

```yaml
lid_suspend: false
```

Default `true` on the drm backend: lys-wm inhibits logind's lid handling and
suspends when the lid is closed, only `eDP-*` outputs are connected, and the
machine is on battery. `false` leaves the lid to logind. Read at startup.

## gpu

```yaml
gpu: card1
```

The rendering DRM card, `cardN` or `/dev/dri/cardN`. Default: the boot VGA
device if it has a render node, else the first card with one. Read at startup.

## Locking

lys-wm implements `ext-session-lock-v1`. While locked only lock surfaces render
and get input, and only `locked` binds run. The lock fades on `fade_in` and
`fade_out`. If the locker dies the session stays locked on a red screen. The
locker, not lys-wm, sets logind's `LockedHint`.

## Session

`lys-wm --systemd`:

- Exports `env`, `PATH`, `WAYLAND_DISPLAY`, `DISPLAY` and `LYS_WM_SOCKET` to
  D-Bus activation and the systemd user manager.
- Starts `lys-wm-session.target`, which binds `graphical-session.target` and
  wants `xdg-desktop-autostart.target`, so XDG autostart entries start
  (`X-GNOME-Autostart-enabled` is ignored; disable with `Hidden=true`).
- On exit stops the target and restores the user manager's environment.

Display manager sessions should use `--systemd`; nested sessions should not.

The package installs `lys-wm-portals.conf`, routing every portal to lys-shell's
backend, falling back to xdg-desktop-portal-wlr for screen sharing and
screenshots and xdg-desktop-portal-gtk for the rest. Portal backends are D-Bus
activated, so they need `--systemd`.
