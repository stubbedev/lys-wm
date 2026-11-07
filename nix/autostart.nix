{
  pkgs,
  module,
}: let
  entry = name: extra: ''
    [Desktop Entry]
    Type=Application
    Name=${name}
    Exec=${pkgs.bash}/bin/sh -c "printenv WAYLAND_DISPLAY > /tmp/autostart-${name}; exec sleep infinity"
    ${extra}
  '';
  started = ["plain" "only-lys-wm" "gnome-enabled-false" "user"];
  skipped = ["hidden" "only-gnome" "not-lys-wm" "systemd-skip" "user-hidden" "overridden"];
  system = {
    plain = "";
    only-lys-wm = "OnlyShowIn=GNOME;lys-wm;";
    hidden = "Hidden=true";
    only-gnome = "OnlyShowIn=GNOME;";
    not-lys-wm = "NotShowIn=lys-wm;";
    gnome-enabled-false = "X-GNOME-Autostart-enabled=false";
    systemd-skip = "X-systemd-skip=true";
    overridden = "";
  };
  user = {
    user = "";
    user-hidden = "Hidden=true";
    overridden = "Hidden=true";
  };
  userEntries = pkgs.lib.mapAttrs' (name: extra:
    pkgs.lib.nameValuePair "/home/alice/.config/autostart/${name}.desktop" {
      "f+" = {
        user = "alice";
        group = "users";
        argument = entry name extra;
      };
    })
  user;
in
  import ./vm.nix {
    inherit pkgs module;
    name = "lys-wm-autostart";
    devices = ["virtio-gpu-pci"];
    outputs = ["Virtual-1"];
    machineConfig = {
      environment.etc = pkgs.lib.mapAttrs' (name: extra: pkgs.lib.nameValuePair "xdg/autostart/${name}.desktop" {text = entry name extra;}) system;
      systemd.tmpfiles.settings.autostart =
        {
          "/home/alice/.config".d = {
            user = "alice";
            group = "users";
          };
          "/home/alice/.config/autostart".d = {
            user = "alice";
            group = "users";
          };
        }
        // userEntries;
    };
    extraTest = run: ''
      display = machine.succeed("ls /run/user/1000 | grep -m1 '^wayland-[0-9]*$'").strip()
      try:
          for name in ${builtins.toJSON started}:
              machine.wait_for_file(f"/tmp/autostart-{name}", timeout=60)
              got = machine.succeed(f"cat /tmp/autostart-{name}").strip()
              assert got == display, f"{name} started with WAYLAND_DISPLAY={got!r}, the session's is {display!r}"
          machine.wait_until_succeeds("${run "systemctl --user is-active xdg-desktop-autostart.target"}", timeout=30)
          for name in ${builtins.toJSON skipped}:
              machine.fail(f"test -e /tmp/autostart-{name}")
      except Exception:
          print(machine.execute("${run "systemctl --user list-units --all 'app-*' xdg-desktop-autostart.target lys-wm-session.target"}; ${run "journalctl --user -b --no-pager"} | tail -80")[1])
          raise
      machine.succeed("${run "systemctl --user show -p After xdg-desktop-autostart.target"} | grep -q lys-wm-session.target")
    '';
  }
