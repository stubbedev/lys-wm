# Scope

## Features

- **Tiling, hy3 style**: a tree of horizontal and vertical splits and tab
  groups, autotile by available width, directional focus and move, numbered
  workspaces per output, a scrolling column layout, floating windows,
  minimizing, an overview, a window switcher, scratchpads, window rules,
  submaps.
- **Drawing**: client surfaces, borders, tab bars and title bars. Rounded
  corners, gaps, soft shadows, spring animations and blur for clients that ask
  are on by default where the renderer supports them
  ([the default look](../config#the-default-look)). Gradient borders,
  opacity, dimming and [screen shaders](../shaders) are opt-in.
- **Desktop protocols**: layer-shell, session lock, idle notify and inhibit,
  data control, screen capture, foreign toplevels, workspaces, output
  management, gamma and colour-matrix control, touchpad gestures, virtual
  input, input methods, sandboxed clients, Xwayland.
- **Games**: Vulkan FIFO pacing (fifo, commit timing), opt-in tearing,
  pointer warps and constraints.
- **Hardware**: DRM/KMS through logind, libinput, NVIDIA (explicit sync) and
  Mesa, fractional scaling, several GPUs.
- **HDR**, per output and off by default: PQ and BT.2020 output,
  `wp_color_manager_v1` for clients, tone mapping both ways. See
  [Config](../config#hdr) and [Architecture](../architecture#hdr).

## Non-goals

Plugins, a scripting language in the config, VR.

## Pure Go

lys-wm builds with `CGO_ENABLED=0`. The few C libraries it needs are loaded at
runtime; see [Architecture](../architecture#pure-go).
