{
  pkgs,
  module,
}: let
  helper = pkgs.runCommandCC "splash-handover" {
    nativeBuildInputs = [pkgs.pkg-config];
    buildInputs = [pkgs.libdrm];
  } "mkdir -p $out/bin && $CC -std=gnu11 -O2 -Wall -Wextra -Werror -o $out/bin/splash-handover ${./splash-handover.c} $(pkg-config --cflags --libs libdrm)";
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-splash-handover";
    devices = ["virtio-gpu-pci"];
    kernelModules = ["vkms"];
    outputs = ["Virtual-1" "Virtual-2"];
    machineConfig = {
      boot.kernelParams = ["drm.debug=0x10" "log_buf_len=64M" "drm_kms_helper.fbdev_emulation=0"];
      environment.systemPackages = [helper];
      systemd.services.splash-handover = {
        requiredBy = ["greetd.service"];
        before = ["greetd.service"];
        after = ["systemd-modules-load.service"];
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
        };
        script = ''
          udevadm settle --timeout=60
          ${helper}/bin/splash-handover preferred
        '';
      };
    };
    extraTest = run: ''
      lys_wmctl_outputs = ${builtins.toJSON (run "lys-wmctl outputs")}
      exec(open("${./splash-handover.py}").read())
    '';
  }
