# Scope

lys-wm is built to replace a Hyprland and hy3 setup with something smaller. This
page is what it does and what it will not do.

## What it does

- **Tiling like hy3**: a tree of horizontal and vertical splits and tab groups,
  autotile by available width, directional focus and move, numbered workspaces
  per output, floating windows, minimizing, window rules, submaps.
- **Drawing**: client surfaces, borders and tab bars. On request, rounded
  corners, gaps, gradient borders, shadows, blur, opacity, dimming,
  animations and [screen shaders](../shaders); [Config](../config) has them, and
  they are all off by default.
- **Desktop protocols** for a full session: layer-shell, session lock, idle
  notify and inhibit, data control, screen capture, foreign toplevels,
  workspaces, output management, gamma and colour-matrix control, touchpad
  gestures, virtual input, input methods, sandboxed clients, Xwayland.
- **Games**: Vulkan FIFO pacing (fifo, commit timing), opt-in tearing, pointer
  warps and constraints.
- **Hardware**: DRM/KMS through logind, libinput, NVIDIA (explicit sync) and
  Mesa, fractional scaling, and several GPUs.
- **HDR**, per output and off by default: PQ and BT.2020 to the display,
  `wp_color_manager_v1` for clients, tone mapping both ways. [Config](../config#hdr)
  has the keys and [Architecture](../architecture#hdr) the design.

## What it will not do

Plugins, a scripting language in the config, VR.

## Pure Go

lys-wm builds with `CGO_ENABLED=0`. The few C libraries with no reasonable
alternative are loaded at runtime; [Architecture](../architecture#pure-go) lists
them and why.
