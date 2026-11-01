<p align="center">
  <img src="docs/public/lys-wm.svg" width="160" alt="lys-wm">
</p>

<h1 align="center">lys-wm</h1>

<p align="center">
  A tiling Wayland compositor in pure Go (<code>CGO_ENABLED=0</code>), built to
  replace a Hyprland + hy3 setup with something smaller.
</p>

<p align="center">
  <a href="https://github.com/stubbedev/lys-wm/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/stubbedev/lys-wm/ci.yml?branch=main&style=for-the-badge&label=CI" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue?style=for-the-badge" alt="MIT license"></a>
  <a href="https://stubbedev.github.io/lys-wm/"><img src="https://img.shields.io/badge/docs-stubbedev.github.io-informational?style=for-the-badge" alt="Documentation"></a>
</p>

hy3-style tiling with splits and tab groups, on DRM/KMS through logind and
libinput, with NVIDIA and Mesa, several GPUs, the protocols a full desktop session
needs, and Xwayland for X11 programs. [Scope](https://stubbedev.github.io/lys-wm/guide/scope)
has the whole list and the non-goals.

**Documentation: <https://stubbedev.github.io/lys-wm/>**

## Install

Debian, Ubuntu and Fedora packages and a tarball, all x86_64, are on the
[releases page](https://github.com/stubbedev/lys-wm/releases):

```sh
sudo apt install ./lys-wm_<version>_amd64.deb
sudo dnf install ./lys-wm-<version>-1.fc<release>.x86_64.rpm
```

On NixOS, add the flake and set `programs.lys-wm.enable = true`.
[Install](https://stubbedev.github.io/lys-wm/guide/install) has the module setup,
home-manager, the tarball and what each needs.

## Use

Pick the `lys-wm` session in your display manager, or run `lys-wm` inside another
Wayland session to get a nested window. `SUPER + Return` runs `foot`, and
`SUPER + Q` closes a window.

```sh
lys-wmctl windows                # query a running lys-wm
lys-wmctl dispatch workspace 3   # or control it
```

[Getting started](https://stubbedev.github.io/lys-wm/guide/getting-started) covers
the default binds and your first config,
[Config](https://stubbedev.github.io/lys-wm/config) every key of
`~/.config/lys-wm/config.yaml`, and
[Contributing](https://stubbedev.github.io/lys-wm/guide/contributing) the
development setup.

## The lys family

lys-wm is the compositor of the lys desktop:
[lys-shell](https://stubbedev.github.io/lys-shell/),
[lys-ui](https://stubbedev.github.io/lys-ui/),
[lys-lib](https://stubbedev.github.io/lys-lib/),
[lys-login](https://stubbedev.github.io/lys-login/),
[lys-splash](https://stubbedev.github.io/lys-splash/),
[lys-polkit](https://stubbedev.github.io/lys-polkit/) and
[lys-secrets](https://stubbedev.github.io/lys-secrets/).
