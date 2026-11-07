{
  pkgs,
  module,
  lys-wm,
  rounds ? 2,
  scenarios ? "window,move,workspace,genie",
  tag ? "",
}: let
  pace = lys-wm.overrideAttrs (old: {
    pname = "lys-wm-pace";
    subPackages = old.subPackages ++ ["cmd/lys-wm-pace"];
    doCheck = false;
  });
  user = "alice";
  paceScript = pkgs.writeShellScript "lys-wm-pace-drm" ''
    ${pace}/bin/lys-wm-pace -compositor ${pace}/bin/lys-wm -backend drm -renderer gles -allow-software -rounds ${toString rounds} -scenarios ${scenarios} -json /tmp/pace.json > /tmp/pace.txt 2>&1
    echo $? > /tmp/pace.status
  '';
  session = {
    command = "${paceScript}";
    inherit user;
  };
in
  import ./vm-test.nix pkgs {
    name = "lys-wm-pace-drm${tag}";
    timeout = 900;
    nodes.machine = {
      imports = [module];
      boot.kernelModules = ["vkms"];
      virtualisation = {
        graphics = true;
        cores = 4;
        memorySize = 3072;
        qemu.options = ["-vga none"];
      };
      hardware.graphics.enable = true;
      programs.lys-wm = {
        enable = true;
        package = pace;
      };
      users.users.${user}.isNormalUser = true;
      services.greetd = {
        enable = true;
        settings = {
          initial_session = session;
          default_session = session;
        };
      };
    };
    testScript = ''
      import datetime as dt

      def diagnose():
          print(machine.execute("cat /tmp/pace.txt; journalctl -b --no-pager -u greetd -t lys-wm | tail -60")[1])

      machine.wait_for_unit("greetd.service")
      try:
          machine.wait_until_succeeds("pgrep -x lys-wm-pace && pgrep -x lys-wm", timeout=dt.timedelta(seconds=60))
          machine.wait_until_succeeds("test -e /tmp/pace.status", timeout=dt.timedelta(minutes=10))
      except Exception:
          diagnose()
          raise
      print(machine.succeed("cat /tmp/pace.txt"))
      machine.copy_from_machine("/tmp/pace.json")
      machine.copy_from_machine("/tmp/pace.txt")
      if machine.succeed("cat /tmp/pace.status").strip() != "0":
          diagnose()
          raise Exception("lys-wm-pace -backend drm failed")
    '';
  }
