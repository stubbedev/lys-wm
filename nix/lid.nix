{
  pkgs,
  module,
}: let
  lidFifo = "/run/lid-switch/lid";
  suspendTag = "lid-suspend";
  primaryConnector = "/sys/class/drm/card0-Virtual-1";
  externalCard = "card1";
  python = pkgs.python3.withPackages (ps: [ps.evdev]);
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-lid";
    devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
    kernelModules = ["test_power" "uinput"];
    outputs = ["Virtual-2"];
    machineConfig = {
      boot.extraModprobeConfig = "options test_power usb_online=off";
      systemd.services = {
        lid-switch = {
          wantedBy = ["multi-user.target"];
          before = ["greetd.service"];
          serviceConfig = {
            RuntimeDirectory = "lid-switch";
            ExecStart = "${python}/bin/python3 ${./lid-switch.py} ${lidFifo}";
          };
        };
        primary-output-off = {
          requiredBy = ["greetd.service"];
          before = ["greetd.service"];
          serviceConfig.Type = "oneshot";
          script = ''
            udevadm wait --settle --timeout=60 ${primaryConnector}
            echo off > ${primaryConnector}/status
          '';
        };
        systemd-suspend.serviceConfig.ExecStart = ["" "${pkgs.util-linux}/bin/logger -t ${suspendTag} suspend"];
      };
    };
    extraTest = run: ''
      lid_fifo = "${lidFifo}"
      suspend_tag = "${suspendTag}"
      external_card = "${externalCard}"
      lys_wmctl_outputs = ${builtins.toJSON (run "lys-wmctl outputs")}
      exec(open("${./lid.py}").read())
    '';
  }
