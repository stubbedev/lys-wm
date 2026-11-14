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
        seconds = 10;
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
    legacyPackages.vm-pace-drm = import ./pace-drm.nix {
      inherit pkgs module;
      inherit (self'.packages) lys-wm;
    };
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
        extraTest = _: ''
          machine.succeed("journalctl -b --no-pager -t lys-wm | grep 'Virtual-2 in .* scans out no linear buffer of the primary GPU, so its frames take a CPU copy'")
        '';
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
        extraTest = _: ''
          machine.succeed("journalctl -b --no-pager -t lys-wm | grep 'reach the screen through the primary GPU.s linear buffers'")
          machine.fail("journalctl -b --no-pager -t lys-wm | grep 'scans out no linear buffer'")
        '';
      };
      vm-hdr-refused = vm {
        name = "lys-wm-hdr-refused";
        devices = ["virtio-gpu-pci"];
        lysWMConfig = ''
          outputs:
            - match: "*"
              hdr: true
        '';
        outputs = ["Virtual-1"];
        extraTest = _: ''
          machine.succeed("journalctl -b --no-pager -t lys-wm | grep 'output Virtual-1 stays SDR'")
        '';
      };
      vm-rtkit = vm {
        name = "lys-wm-rtkit";
        devices = ["virtio-gpu-pci"];
        outputs = ["Virtual-1"];
        machineConfig = {lib, ...}: {
          security.pam.loginLimits = lib.mkForce [];
          security.rtkit.enable = true;
        };
        extraTest = run: ''
          [compositor] = machine.succeed("pgrep -x lys-wm").split()
          machine.succeed(f"grep -E '^Max realtime priority +0 +0 ' /proc/{compositor}/limits")
          machine.succeed(f"grep -E '^Max nice priority +0 +0 ' /proc/{compositor}/limits")
          machine.succeed("journalctl -b --no-pager -t lys-wm | grep 'the compositor thread runs at SCHED_RR through rtkit'")
          machine.succeed(f"grep -E '^Max realtime timeout +[0-9]+ +[0-9]+ ' /proc/{compositor}/limits")
          rr = f"ps -L -o cls=,rtprio= -p {compositor} | grep -Eq '^ *RR +1$'"
          def realtime():
              try:
                  machine.wait_until_succeeds(rr, timeout=30)
              except Exception:
                  print(machine.execute(f"ps -L -o tid=,cls=,rtprio= -p {compositor}; journalctl -b --no-pager -t lys-wm -t rtkit-daemon")[1])
                  raise
          realtime()
          machine.succeed(f"ps -L -o cls=,rtprio= -p {compositor} | grep -Eq '^ *RR +2$'")
          demoted = "journalctl -b --no-pager -t lys-wm | grep -E 'the compositor thread ran [^ ]+ at SCHED_RR without blocking, past the 20ms its guard allows under RLIMIT_RTTIME'"
          ordinary = machine.execute(demoted)[1]
          if ordinary:
              print(machine.execute("journalctl -b --no-pager -t lys-wm -t rtkit-daemon")[1])
          assert not ordinary, f"the compositor thread was demoted while it drew ordinary frames: {ordinary}"
          threads = f"ps -L -o cls=,rtprio= -p {compositor} | grep -Ec '^ *RR +1$'"
          raised = machine.succeed(threads).strip()
          machine.succeed("${run "lys-wmctl dispatch debug spin 500"}")
          try:
              machine.wait_until_succeeds(f"{demoted} | grep -c . | grep -qx 1", timeout=30)
              machine.succeed("journalctl -b --no-pager -t lys-wm | grep -E 'internal/compositor\\.spinPreemptibly\\('")
              assert machine.succeed("pgrep -x lys-wm").split() == [compositor], "lys-wm did not survive a 500 ms realtime spin under rtkit's 200 ms RLIMIT_RTTIME"
              machine.wait_until_succeeds("journalctl -b --no-pager -t lys-wm | grep -q 'the compositor thread runs at SCHED_RR again'", timeout=30)
              machine.wait_until_succeeds(f"{threads} | grep -qx {raised}", timeout=30)
          except Exception:
              print(machine.execute(f"ps -L -o tid=,cls=,rtprio= -p {compositor}; journalctl -b --no-pager -t lys-wm -t rtkit-daemon")[1])
              raise
        '';
      };
      vm-screencast = screencast "wlr";
      vm-apps = import ./apps.nix {inherit pkgs module;};
      vm-upgrade = import ./upgrade.nix {inherit pkgs module;};
      vm-cursor = import ./cursor.nix {inherit pkgs module;};
      vm-seat = import ./seat.nix {
        inherit pkgs;
        inherit (self'.packages) lys-wm;
      };
      vm-lid = import ./lid.nix {inherit pkgs module;};
      vm-autostart = import ./autostart.nix {inherit pkgs module;};
      vm-splash-handover = import ./splash-handover.nix {inherit pkgs module;};
      vm-xwayland = import ./xwayland.nix {inherit pkgs module;};
      vm-xdnd = import ./xdnd.nix {inherit pkgs module;};
      vm-xwayland-apps = xwaylandApp "idea";
      vm-gpu-tests = import ./gpu-tests.nix {
        inherit pkgs;
        inherit (self'.packages) lys-wm;
        inherit (inputs'.lys-ui.packages) lys-ui;
      };
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
        assert machine.config.programs.foot.enable;
        assert machine.config.xdg.portal.wlr.enable;
        assert builtins.elem pkgs.xdg-desktop-portal-gtk machine.config.xdg.portal.extraPortals;
        assert builtins.elem "lys-wm" machine.config.services.displayManager.sessionData.sessionNames;
          pkgs.runCommand "lys-wm-module" {} ''
            grep -q "^Exec=${self'.packages.lys-wm}/bin/lys-wm --systemd$" ${sessions}/share/wayland-sessions/lys-wm.desktop
            touch $out
          '';
    };
  };
}
