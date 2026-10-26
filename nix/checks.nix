{
  inputs,
  self,
  ...
}: {
  perSystem = {
    inputs',
    pkgs,
    self',
    system,
    ...
  }: let
    module = self.nixosModules.default;
    vm = args: import ./vm.nix ({inherit pkgs module;} // args);
    screencast = portal:
      import ./screencast.nix {
        inherit pkgs module portal;
        lysShell = inputs.lys-shell.nixosModules.default;
        seconds = 60;
      };
    xwaylandApp = app:
      import ./xwayland-apps.nix {
        inherit module app;
        pkgs = import inputs.nixpkgs {
          inherit system;
          config.allowUnfreePredicate = p: builtins.elem (pkgs.lib.getName p) ["intellij-idea" "steam" "steam-unwrapped"];
        };
      };
  in {
    legacyPackages.vm-steam = xwaylandApp "steam";
    checks = {
      vm = vm {
        name = "lys-wm-drm";
        devices = ["virtio-gpu-pci"];
        outputs = ["Virtual-1"];
      };
      vm-two-gpus = vm {
        name = "lys-wm-two-gpus";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        outputs = ["Virtual-1" "Virtual-2"];
        unplug = {
          card = "card1";
          outputs = ["Virtual-1"];
        };
      };
      vm-configured-gpu = vm {
        name = "lys-wm-configured-gpu";
        devices = ["virtio-gpu-pci" "virtio-gpu-pci"];
        lysWMConfig = "gpu: card1";
        rendersOn = "card1";
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-display-only = vm {
        name = "lys-wm-display-only";
        devices = ["virtio-gpu-pci"];
        kernelModules = ["vkms"];
        outputs = ["Virtual-1" "Virtual-2"];
      };
      vm-screencast = screencast "wlr";
      vm-screencast-lys-shell = screencast "lys-shell";
      vm-apps = import ./apps.nix {inherit pkgs module;};
      vm-cursor = import ./cursor.nix {inherit pkgs module;};
      vm-xwayland = import ./xwayland.nix {inherit pkgs module;};
      vm-xwayland-apps = xwaylandApp "idea";
      headless = pkgs.callPackage ./headless.nix {
        inherit (self'.packages) lys-wm;
        inherit (inputs'.lys-ui.packages) lys-ui;
      };
      module = let
        machine = inputs.nixpkgs.lib.nixosSystem {
          modules = [
            module
            {
              nixpkgs.hostPlatform = system;
              programs.lys-wm.enable = true;
              services.greetd.enable = true;
              boot.loader.grub.enable = false;
              fileSystems."/".device = "nodev";
              system.stateVersion = "25.11";
            }
          ];
        };
        sessions = machine.config.services.displayManager.sessionData.desktops;
      in
        assert builtins.elem self'.packages.lys-wm machine.config.xdg.portal.configPackages;
        assert builtins.elem "lys-wm" machine.config.services.displayManager.sessionData.sessionNames;
          pkgs.runCommand "lys-wm-module" {} ''
            grep -q "^Exec=${self'.packages.lys-wm}/bin/lys-wm --systemd$" ${sessions}/share/wayland-sessions/lys-wm.desktop
            touch $out
          '';
    };
  };
}
