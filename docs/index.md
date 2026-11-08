---
layout: home

hero:
  name: lys-wm
  text: A tiling Wayland compositor in pure Go
  tagline: hy3-style tiling on DRM/KMS through logind and libinput, the protocols a full desktop session needs, and Xwayland for X11 programs. No cgo.
  image:
    src: /lys-wm.svg
    alt: lys-wm
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Config
      link: /config
    - theme: alt
      text: GitHub
      link: https://github.com/stubbedev/lys-wm

features:
  - title: Tiling like hy3
    details: A tree of horizontal and vertical splits and tab groups, autotile by available width, directional focus and move, floating windows, window rules and submaps.
  - title: A full desktop session
    details: Layer-shell, session lock, idle, data control, screen capture, foreign toplevels, workspaces, output management, input methods, sandboxed clients and Xwayland.
  - title: Games and hardware
    details: Vulkan FIFO pacing, opt-in tearing, pointer warps and constraints, NVIDIA explicit sync and Mesa, fractional scaling, and outputs on several GPUs.
  - title: A finished look by default
    details: Rounded corners, gaps, soft shadows, borders and spring animations out of the box, with blur behind bars and dropdowns on a GPU. Each is a config key.
  - title: One config file, one socket
    details: One validated YAML file whose errors name file, line and key, and lys-wmctl with a JSON protocol and event stream.
  - title: Packaged
    details: A NixOS module, a home-manager module, and Debian, Ubuntu, Fedora and tarball release builds.
---
