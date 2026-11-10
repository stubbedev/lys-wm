{
  pkgs,
  module,
}: let
  launch = import ./launch.nix pkgs;
  dragon = backend: args: "${pkgs.coreutils}/bin/env GDK_BACKEND=${backend} ${pkgs.dragon-drop}/bin/dragon-drop ${args}";
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-xdnd";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {
      environment.systemPackages = [pkgs.xorg.xwininfo pkgs.xdotool];
      fonts.packages = [pkgs.dejavu_fonts];
    };
    extraTest = run: let
      params = builtins.toJSON {
        as_alice = run "{}";
        xwindows = import ./xwindows.nix pkgs;
        launchers = {
          wayland-source = launch "wayland-source" (dragon "wayland" "/tmp/from-wayland.txt");
          x-target = launch "x-target" (dragon "x11" "--target --and-exit --print-path");
          x-source = launch "x-source" (dragon "x11" "/tmp/from-x.txt");
          wayland-target = launch "wayland-target" (dragon "wayland" "--target --and-exit --print-path");
        };
      };
    in
      ''
        import json
        params = json.loads(${builtins.toJSON params})
      ''
      + import ./vm-scripts.nix [./xwayland-lib.py ./xdnd.py];
  }
