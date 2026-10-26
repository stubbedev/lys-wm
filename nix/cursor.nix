{
  pkgs,
  module,
}:
import ./vm.nix {
  inherit pkgs module;
  name = "lys-wm-cursor";
  devices = ["virtio-gpu-pci" "virtio-mouse-pci"];
  kernelModules = ["vkms"];
  outputs = ["Virtual-1" "Virtual-2"];
  framedCursors = ["Virtual-2"];
  machineConfig = {
    boot.extraModprobeConfig = "options vkms enable_cursor=0";
    environment.systemPackages = [pkgs.drm_info];
  };
  extraTest = _: ''
    exec(open("${./cursor.py}").read())
  '';
}
