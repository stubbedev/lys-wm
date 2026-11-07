{
  pkgs,
  lys-ui,
}:
with pkgs; [
  just
  wayland-utils
  sway
  grim
  libxkbcommon
  weston
  foot
  wl-clipboard
  xinput
  dbus
  mesa-demos
  glslang
  spirv-tools
  gtk3.dev
  gtk4.dev
  (import ./lys-ui-debug.nix lys-ui)
  (import ./alacritty-swrast.nix pkgs)
]
