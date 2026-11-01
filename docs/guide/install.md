# Install

Release builds are on the
[releases page](https://github.com/stubbedev/lys-wm/releases): a Debian package,
an RPM and a tarball for x86_64, with a `SHA256SUMS` file. The Nix flake builds
from source for x86_64 and aarch64, though CI only builds x86_64. There is no apt
or dnf repository, so packages do not update themselves.

Every install gets the `lys-wm` and `lys-wmctl` binaries, the `lys-wm` session
for display managers, the `lys-wm-session.target` user unit and the
`lys-wm-portals.conf` [portal routing](../config#session).

## NixOS

Add the flake as an input and enable the module:

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

`programs.lys-wm.enable` installs lys-wm, offers it as a session to display
managers such as greetd (`lys-wm --systemd`), enables the portals that
`lys-wm-portals.conf` falls back to, polkit, `foot` for the built-in
`SUPER + Return` bind and the rest of a Wayland session; set
`programs.foot.enable = false` to leave it out.
lys-shell, which the portal config prefers, is yours to add. GPU drivers come
from `/run/opengl-driver`, as for every NixOS compositor.
`programs.lys-wm.package` picks another build.

## Nix on another distribution

With standalone home-manager, import `lys-wm.homeManagerModules.default` and set
`programs.lys-wm.enable = true`. It installs lys-wm and `foot` (set
`programs.foot.enable = false` to leave it out), and links
`lys-wm-session.target` into `~/.config/systemd/user`, because the user manager
there does not search the Nix profile for units. The display manager, portals
and GPU drivers stay the host's, so point the host's display manager at
`lys-wm --systemd` yourself.

To try lys-wm nested in the Wayland session you are in, without installing it:

```sh
nix run github:stubbedev/lys-wm
```

If it cannot use your GPU driver from there, it says so; render in software with
`nix run github:stubbedev/lys-wm -- --renderer soft`.

## Debian and Ubuntu

```sh
sudo apt install ./lys-wm_<version>_amd64.deb
```

The package depends on `libegl1`, `libgles2`, `libgbm1`, `libinput10`,
`libxkbcommon0`, `hwdata` and `fonts-dejavu-core`. It recommends `xwayland`,
`xdg-desktop-portal`, `xdg-desktop-portal-wlr`, `xdg-desktop-portal-gtk`,
`lys-shell`, `foot`, `libvulkan1` and `mesa-vulkan-drivers`. CI installs and runs
it on `ubuntu-latest`.

`lys-shell` is not in the distribution's archive: take its package from
[its releases](https://github.com/stubbedev/lys-shell/releases). Without it,
`xdg-desktop-portal-wlr` serves screen sharing and screenshots and
`xdg-desktop-portal-gtk` the other portals.

## Fedora

```sh
sudo dnf install ./lys-wm-<version>-1.fc<release>.x86_64.rpm
```

The package requires `libglvnd-egl`, `libglvnd-gles`, `mesa-libgbm`, `libinput`,
`libxkbcommon`, `hwdata` and `dejavu-sans-fonts`. It recommends
`xorg-x11-server-Xwayland`, `xdg-desktop-portal`, `xdg-desktop-portal-wlr`,
`xdg-desktop-portal-gtk`, `lys-shell`, `foot`, `vulkan-loader` and
`mesa-vulkan-drivers`. It is built, installed and run on `fedora:latest`, and
the file name carries that release. What the Debian section says about
`lys-shell` and the portal backends holds here too.

## Other distributions

There is no package for other distributions. The tarball holds the two binaries,
the session file, the user unit, the portal config and the licences:

```sh
tar xzf lys-wm-<version>-x86_64-linux.tar.gz
cd lys-wm-<version>-x86_64-linux
sudo install -m 0755 lys-wm lys-wmctl /usr/bin/
sudo install -Dm 0644 lys-wm-session.target /usr/lib/systemd/user/lys-wm-session.target
sudo install -Dm 0644 lys-wm-portals.conf /usr/share/xdg-desktop-portal/lys-wm-portals.conf
sudo install -Dm 0644 lys-wm.desktop /usr/share/wayland-sessions/lys-wm.desktop
```

The binaries need a glibc loader at `/lib64/ld-linux-x86-64.so.2`, and these from
your distribution:

| What | Where lys-wm looks | Override |
| --- | --- | --- |
| EGL | `libEGL.so.1` | `LYS_WM_LIBEGL` |
| GLES2 | `libGLESv2.so.2` | `LYS_WM_LIBGLESV2` |
| GBM | `libgbm.so.1` | `LYS_WM_LIBGBM` |
| libinput | `libinput.so.10` | `LYS_WM_LIBINPUT` |
| libxkbcommon | `libxkbcommon.so.0` | `LYS_WM_LIBXKBCOMMON` |
| PNP IDs, from hwdata | `/usr/share/hwdata/pnp.ids` | `LYS_PNP_IDS` |
| Tab font | `/usr/share/lys-wm/DejaVuSans.ttf` | `tabs.font` in the config, or `LYS_WM_FONT` |
| Xwayland, for X11 programs | `Xwayland` on `PATH` | `LYS_WM_XWAYLAND` |
| Vulkan, for `--renderer vulkan` | `libvulkan.so.1` | `LYS_LIBVULKAN` |

The packages link the tab font to the distribution's DejaVu Sans, so do the same
or name another font. The overrides work on every install.

## Check it

```sh
lys-wm -backend none     # prints: unknown backend "none" (available: drm, nested, headless)
lys-wmctl                # prints an error: no command
```

Both exit 1. That is the smoke test CI runs on the packages, and it shows that
the binaries load. Then [start a session](./getting-started).
