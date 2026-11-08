# Install

Release builds for x86_64 and aarch64 are on the
[releases page](https://github.com/stubbedev/lys-wm/releases): a Debian
package, an RPM and a tarball, with `SHA256SUMS`. There is no apt or dnf
repository, so packages do not update themselves. The Nix flake builds from
source.

Every install provides:

- `lys-wm` and `lys-wmctl`
- the `lys-wm` display manager session (`lys-wm --systemd`)
- the `lys-wm-session.target` systemd user unit
- `lys-wm-portals.conf`, the [portal routing](../config#session)
- login limits (`rtprio 1`, `nice -10`) so the compositor thread can run
  with real-time priority

## NixOS

```nix
{
  inputs.lys-wm.url = "github:stubbedev/lys-wm";
  outputs = {nixpkgs, lys-wm, ...}: {
    nixosConfigurations.host = nixpkgs.lib.nixosSystem {
      modules = [
        lys-wm.nixosModules.default
        {programs.lys-wm.enable = true;}
      ];
    };
  };
}
```

| Option | Default | Effect |
| --- | --- | --- |
| `programs.lys-wm.enable` | `false` | Install lys-wm, register the session, the user unit and portal config, set the login limits, enable `foot` and the NixOS Wayland session defaults (polkit, portals) |
| `programs.lys-wm.package` | the flake's build | The lys-wm package |

Set `programs.foot.enable = false` to leave out `foot`. lys-shell, which the
portal config prefers, is not included. GPU drivers come from
`/run/opengl-driver`.

## home-manager on another distribution

Import `lys-wm.homeManagerModules.default` and set
`programs.lys-wm.enable = true` (same `package` option). It installs lys-wm
and `foot`, and links `lys-wm-session.target` into `~/.config/systemd/user`.
The display manager, portals, login limits and GPU drivers stay the host's:
point the display manager at `lys-wm --systemd` yourself.

To try lys-wm nested in your current Wayland session without installing it:

```sh
nix run github:stubbedev/lys-wm
nix run github:stubbedev/lys-wm -- --renderer soft   # if the GPU driver is unusable from Nix
```

## Debian and Ubuntu

```sh
sudo apt install ./lys-wm_<version>_amd64.deb    # _arm64.deb on aarch64
```

- Depends: `libegl1`, `libgles2`, `libgbm1`, `libinput10`, `xkb-data`,
  `hwdata`, `fonts-dejavu-core`.
- Recommends: `xwayland`, `xdg-desktop-portal`, `xdg-desktop-portal-wlr`,
  `xdg-desktop-portal-gtk`, `lys-shell`, `foot`, `libvulkan1`,
  `mesa-vulkan-drivers`.

`lys-shell` is not in the distribution archive; get it from
[its releases](https://github.com/stubbedev/lys-shell/releases). Without it,
`xdg-desktop-portal-wlr` serves screen sharing and screenshots and
`xdg-desktop-portal-gtk` the other portals.

## Fedora

```sh
sudo dnf install ./lys-wm-<version>-1.fc<release>.x86_64.rpm    # .aarch64.rpm on aarch64
```

- Requires: `libglvnd-egl`, `libglvnd-gles`, `mesa-libgbm`, `libinput`,
  `xkeyboard-config`, `hwdata`, `dejavu-sans-fonts`.
- Recommends: `xorg-x11-server-Xwayland`, `xdg-desktop-portal`,
  `xdg-desktop-portal-wlr`, `xdg-desktop-portal-gtk`, `lys-shell`, `foot`,
  `vulkan-loader`, `mesa-vulkan-drivers`.

The RPM is built for the current `fedora:latest`. The lys-shell and portal
notes above apply.

## Tarball

The tarball holds the binaries, session file, user unit, portal config,
limits file and licences. `<arch>` is `x86_64` or `aarch64`:

```sh
tar xzf lys-wm-<version>-<arch>-linux.tar.gz
cd lys-wm-<version>-<arch>-linux
sudo install -m 0755 lys-wm lys-wmctl /usr/bin/
sudo install -Dm 0644 lys-wm-session.target /usr/lib/systemd/user/lys-wm-session.target
sudo install -Dm 0644 lys-wm-portals.conf /usr/share/xdg-desktop-portal/lys-wm-portals.conf
sudo install -Dm 0644 lys-wm.desktop /usr/share/wayland-sessions/lys-wm.desktop
sudo install -Dm 0644 90-lys-wm.conf /etc/security/limits.d/90-lys-wm.conf
```

The binaries need the glibc loader (`/lib64/ld-linux-x86-64.so.2` or
`/lib/ld-linux-aarch64.so.1`) and these runtime dependencies. The overrides
work on every install.

| Dependency | Default | Override |
| --- | --- | --- |
| EGL | `libEGL.so.1` | `LYS_WM_LIBEGL` |
| GLES2 | `libGLESv2.so.2` | `LYS_WM_LIBGLESV2` |
| GBM | `libgbm.so.1` | `LYS_WM_LIBGBM` |
| libinput | `libinput.so.10` | `LYS_WM_LIBINPUT` |
| xkeyboard-config | `/usr/share/X11/xkb` | `XKB_CONFIG_ROOT` |
| PNP IDs (hwdata) | `/usr/share/hwdata/pnp.ids` | `LYS_PNP_IDS` |
| Tab font | `/usr/share/lys-wm/DejaVuSans.ttf` | `tabs.font` in the config, or `LYS_WM_FONT` |
| Xwayland | `Xwayland` on `PATH` | `LYS_WM_XWAYLAND` |
| Vulkan (`--renderer vulkan`) | `libvulkan.so.1` | `LYS_LIBVULKAN` |

The packages symlink the tab font to the distribution's DejaVu Sans; do the
same or set another font.

## Check it

```sh
lys-wm -backend none     # unknown backend "none" (available: drm, nested, headless)
lys-wmctl                # no command
```

Both exit 1, which shows the binaries load. Then
[start a session](./getting-started).
