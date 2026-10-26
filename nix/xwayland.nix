{
  pkgs,
  module,
}: let
  launch = import ./launch.nix pkgs;
  checker = pkgs.runCommand "checker.ppm" {} ''
    ${pkgs.python3}/bin/python3 -c '
    import sys
    size = 64
    pixels = b"".join(b"\xff\x00\x00" if (x + y) % 2 == 0 else b"\x00\x00\xff" for y in range(size) for x in range(size))
    sys.stdout.buffer.write(b"P6\n%d %d\n255\n" % (size, size) + pixels)
    ' > $out
  '';
  feh = name: launch name "${pkgs.feh}/bin/feh --class ${name} --image-bg black ${checker}";
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-xwayland";
    devices = ["virtio-gpu-pci,id=gpu0" "virtio-gpu-pci,id=gpu1"];
    outputs = ["Virtual-1" "Virtual-2"];
    lysWMConfig = ''
      outputs:
        - {match: Virtual-1, scale: 1.5}
        - {match: Virtual-2, scale: 1}
    '';
    machineConfig = {
      environment.systemPackages = [pkgs.xorg.xwininfo pkgs.xdotool];
    };
    extraTest = run: let
      params = builtins.toJSON {
        as_alice = run "{}";
        xwindows = import ./xwindows.nix pkgs;
        wlrctl = pkgs.lib.getExe pkgs.wlrctl;
        heads = {
          Virtual-1 = "gpu0";
          Virtual-2 = "gpu1";
        };
        launchers = {
          Virtual-1 = feh "checker-hidpi";
          Virtual-2 = feh "checker-plain";
        };
      };
    in ''
      import json
      params = json.loads(${builtins.toJSON params})
      exec(open("${./xwayland-lib.py}").read())
      exec(open("${./xwayland.py}").read())
    '';
  }
